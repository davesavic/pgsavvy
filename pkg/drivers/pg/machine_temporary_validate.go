//go:build linux || darwin

package pg

import (
	"context"
	"errors"
	"reflect"
	"strings"

	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/lexbase"
	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/parser"
	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/sem/tree"

	"github.com/davesavic/pgsavvy/pkg/query"
)

// TemporaryStatement describes a half-open original byte range, excluding its
// separator but retaining whitespace/comments. Writes includes nested CTEs.
type TemporaryStatement struct {
	Start, End int
	Writes     bool
}

// ValidatedTemporaryScript can only be constructed by ValidateTemporaryScript.
// Its zero value is not executable. SQL must be sent once, not range by range,
// with UTF8 and standard_conforming_strings=on established by the executor.
type ValidatedTemporaryScript struct {
	sql            string
	statements     []TemporaryStatement
	parameterCount int
	writes         bool
	allowWrite     bool
	validated      bool
}

func (s ValidatedTemporaryScript) SQL() string              { return s.sql }
func (s ValidatedTemporaryScript) HasWrites() bool          { return s.writes }
func (s ValidatedTemporaryScript) AllowsWrites() bool       { return s.allowWrite }
func (s ValidatedTemporaryScript) ParameterCount() int      { return s.parameterCount }
func (s ValidatedTemporaryScript) SupportsParameters() bool { return len(s.statements) == 1 }

func (s ValidatedTemporaryScript) Statements() []TemporaryStatement {
	return append([]TemporaryStatement(nil), s.statements...)
}

// ValidateTemporaryScript validates the entire script before returning any
// executable artifact. It is not a sandbox for functions, triggers or extensions.
// Parameter values and result limits remain the executor's responsibility.
func ValidateTemporaryScript(ctx context.Context, sql string, parameterCount int, allowWrite bool) (ValidatedTemporaryScript, error) {
	deny := func(code string) (ValidatedTemporaryScript, error) {
		return ValidatedTemporaryScript{}, &MachineError{Code: code}
	}
	if ctx.Err() != nil {
		return deny("TIMEOUT")
	}
	if parameterCount < 0 || parameterCount > 256 {
		return deny("INVALID_INPUT")
	}
	// Only use the existing analyzer for original boundaries and input policy;
	// its heuristic Kind and approval classifications are not safety evidence.
	analysis, err := query.AnalyzeCLIStatements(ctx, sql, parameterCount, false, false)
	if err != nil {
		if ctx.Err() != nil {
			return deny("TIMEOUT")
		}
		if errors.Is(err, query.ErrInvalidCLIInput) {
			return deny("INVALID_INPUT")
		}
		return deny("UNSUPPORTED_SQL")
	}
	if err := checkTemporaryLexical(ctx, sql); err != nil {
		return ValidatedTemporaryScript{}, err
	}
	result := ValidatedTemporaryScript{sql: sql, parameterCount: parameterCount, allowWrite: allowWrite}
	for _, part := range analysis.Statements {
		if ctx.Err() != nil {
			return deny("TIMEOUT")
		}
		parsed, parseErr := parser.ParseOne(sql[part.Start:part.End])
		// The pinned parser has no cancellation API. Bound its input above and
		// never publish its result after the caller's deadline.
		if ctx.Err() != nil {
			return deny("TIMEOUT")
		}
		if parseErr != nil {
			return deny("UNSUPPORTED_SQL")
		}
		writes := false
		if err := checkTemporaryAST(ctx, reflect.ValueOf(parsed.AST), 0, &writes); err != nil {
			return ValidatedTemporaryScript{}, err
		}
		result.statements = append(result.statements, TemporaryStatement{Start: part.Start, End: part.End, Writes: writes})
		result.writes = result.writes || writes
	}
	if ctx.Err() != nil {
		return deny("TIMEOUT")
	}
	if result.writes && !allowWrite {
		return deny("POLICY_DENIED")
	}
	result.validated = true
	return result, nil
}

// Like the saved guarded traversal, visit every exported child rather than the
// dependency's incomplete expression walker (notably JOIN ON and window frames).
func checkTemporaryAST(ctx context.Context, value reflect.Value, depth int, writes *bool) error {
	if ctx.Err() != nil {
		return &MachineError{Code: "TIMEOUT"}
	}
	deny := func() error { return &MachineError{Code: "UNSUPPORTED_SQL"} }
	if depth > 512 {
		return deny()
	}
	if !value.IsValid() || (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) && value.IsNil() {
		return nil
	}
	if value.CanInterface() {
		node := value.Interface()
		if statement, ok := node.(tree.Statement); ok {
			switch statement.(type) {
			case *tree.Select, *tree.SelectClause, *tree.ParenSelect, *tree.UnionClause, *tree.ValuesClause:
			case *tree.Insert, *tree.Update, *tree.Delete:
				*writes = true
			default:
				return deny()
			}
		}
		if table, ok := node.(tree.TableExpr); ok {
			switch table.(type) {
			case *tree.AliasedTableExpr, *tree.ParenTableExpr, *tree.JoinTableExpr, *tree.RowsFromExpr, *tree.Subquery, *tree.TableName, *tree.UnresolvedObjectName:
			default:
				return deny()
			}
		}
		if returning, ok := node.(tree.ReturningClause); ok {
			switch returning.(type) {
			case *tree.NoReturningClause, *tree.ReturningExprs:
			default:
				return deny()
			}
		}
		switch node := node.(type) {
		case *tree.Select:
			if len(node.Locking) != 0 {
				return deny()
			}
		case *tree.UnionClause:
			if node.Type != tree.UnionOp && node.Type != tree.IntersectOp && node.Type != tree.ExceptOp {
				return deny()
			}
		case *tree.Insert:
			if node.OnConflict.IsUpsertAlias() {
				return deny()
			}
		case *tree.Update:
			if len(node.OrderBy) != 0 || node.Limit != nil {
				return deny()
			}
		case *tree.Delete:
			if len(node.OrderBy) != 0 || node.Limit != nil {
				return deny()
			}
		case *tree.AliasedTableExpr:
			if node.IndexFlags != nil {
				return deny()
			}
		case *tree.JoinTableExpr:
			if node.Hint != "" {
				return deny()
			}
		case tree.From:
			if node.AsOf.Expr != nil {
				return deny()
			}
		case *tree.Order:
			if node.OrderType != tree.OrderByColumn {
				return deny()
			}
		}
	}
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		return checkTemporaryAST(ctx, value.Elem(), depth+1, writes)
	case reflect.Slice, reflect.Array:
		for i := 0; i < value.Len(); i++ {
			if err := checkTemporaryAST(ctx, value.Index(i), depth+1, writes); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			if value.Type().Field(i).PkgPath == "" {
				if err := checkTemporaryAST(ctx, value.Field(i), depth+1, writes); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// The pinned scanner is not PostgreSQL's lexer: line comments end only at LF,
// lowercase b strings have byte escapes, identifiers are Unicode-normalized,
// and experimental_/testing_ keywords are aliases. Exclude these differences
// here without changing the saved/native boundary analyzer. Prefix U&, N, B/X
// literals and string continuation are outside this conservative lexical subset.
func checkTemporaryLexical(ctx context.Context, sql string) error {
	deny := func() error { return &MachineError{Code: "UNSUPPORTED_SQL"} }
	identStart := func(b byte) bool { return lexbase.IsIdentStart(int(b)) }
	for i := 0; i < len(sql); {
		if ctx.Err() != nil {
			return &MachineError{Code: "TIMEOUT"}
		}
		switch {
		case strings.HasPrefix(sql[i:], "--"):
			i += 2
			for i < len(sql) && sql[i] != '\n' {
				if sql[i] == '\r' && (i+1 == len(sql) || sql[i+1] != '\n') {
					return deny()
				}
				i++
			}
		case strings.HasPrefix(sql[i:], "/*"):
			i += 2
			for depth := 1; depth != 0; {
				if ctx.Err() != nil {
					return &MachineError{Code: "TIMEOUT"}
				}
				if i == len(sql) {
					return deny()
				}
				switch {
				case strings.HasPrefix(sql[i:], "/*"):
					depth++
					i += 2
				case strings.HasPrefix(sql[i:], "*/"):
					depth--
					i += 2
				default:
					i++
				}
			}
		case sql[i] == '\'' || sql[i] == '"' || (sql[i] == 'e' || sql[i] == 'E') && i+1 < len(sql) && sql[i+1] == '\'':
			escapes := sql[i] == 'e' || sql[i] == 'E'
			if escapes {
				i++
			}
			quote := sql[i]
			i++
			closed := false
			for i < len(sql) {
				if ctx.Err() != nil {
					return &MachineError{Code: "TIMEOUT"}
				}
				if escapes && sql[i] == '\\' {
					i++
					// Restrict escapes to the common PostgreSQL/Go spelling set;
					// the native analyzer validates decoded UTF8/NUL separately.
					if i == len(sql) || !strings.ContainsRune("\\'\"bfnrtxuU01234567", rune(sql[i])) {
						return deny()
					}
					i++
				} else if sql[i] == quote {
					i++
					if i < len(sql) && sql[i] == quote {
						i++
						continue
					}
					closed = true
					break
				} else {
					i++
				}
			}
			if !closed {
				return deny()
			}
			// Both lexers concatenate across LF whitespace, but differ for
			// comments, CR, and quoted identifiers. Reject adjacent quotes.
			j := i
			for j < len(sql) && strings.ContainsRune(" \t\r\n\f\v", rune(sql[j])) {
				j++
			}
			if j < len(sql) && sql[j] == quote {
				return deny()
			}
		case sql[i] == '$':
			j := i + 1
			if j < len(sql) && identStart(sql[j]) {
				for j < len(sql) && sql[j] != '$' && lexbase.IsIdentMiddle(int(sql[j])) {
					j++
				}
			}
			if j < len(sql) && sql[j] == '$' {
				delimiter := sql[i : j+1]
				end := strings.Index(sql[j+1:], delimiter)
				if end < 0 {
					return deny()
				}
				i = j + 1 + end + len(delimiter)
			} else {
				i++
			}
		case identStart(sql[i]):
			start := i
			for i < len(sql) && lexbase.IsIdentMiddle(int(sql[i])) {
				i++
			}
			word := sql[start:i]
			folded := []byte(word)
			for j, b := range folded {
				if b >= 'A' && b <= 'Z' {
					folded[j] += 'a' - 'A'
				}
			}
			if lexbase.NormalizeName(word) != string(folded) || strings.HasPrefix(string(folded), "experimental_") || strings.HasPrefix(string(folded), "testing_") {
				return deny()
			}
			if i < len(sql) && sql[i] == '\'' || (word == "u" || word == "U") && i+2 < len(sql) && sql[i] == '&' && (sql[i+1] == '\'' || sql[i+1] == '"') {
				return deny()
			}
		default:
			// CR is harmless outside line comments; VT is not scanner whitespace.
			if sql[i] == '\v' {
				return deny()
			}
			i++
		}
	}
	if ctx.Err() != nil {
		return &MachineError{Code: "TIMEOUT"}
	}
	return nil
}
