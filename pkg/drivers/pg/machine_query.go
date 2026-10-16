//go:build linux

package pg

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/parser"
	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/sem/tree"

	"github.com/davesavic/pgsavvy/pkg/models"
)

type (
	MachineQuery struct {
		SQL        string
		Parameters [][]byte
		MaxRows    int
		Schema     string
	}
	MachineColumn struct {
		Name     string `json:"name"`
		TypeOID  uint32 `json:"type_oid"`
		Encoding string `json:"encoding"`
	}
	MachineQueryResult struct {
		Columns   []MachineColumn `json:"columns"`
		Rows      [][]*string     `json:"rows"`
		Truncated bool            `json:"-"`
	}
)

func ValidateMachineQuery(ctx context.Context, sql string) error {
	if len(sql) == 0 || len(sql) > 1<<20 || !utf8.ValidString(sql) || strings.TrimSpace(sql) == "" {
		return &MachineError{Code: "INVALID_INPUT"}
	}
	if ctx.Err() != nil {
		return &MachineError{Code: "TIMEOUT"}
	}
	parsed, err := parser.ParseOne(sql)
	if err != nil {
		return &MachineError{Code: "UNSUPPORTED_SQL"}
	}
	if _, ok := parsed.AST.(*tree.Select); !ok {
		return &MachineError{Code: "UNSUPPORTED_SQL"}
	}
	return checkMachineAST(ctx, reflect.ValueOf(parsed.AST), 0)
}

// The dependency's expression walker omits JOIN ON and inline window children.
// Traverse exported AST children without rewriting, with closed statement/table
// type checks at every node, including CTEs and nested statement sources.
func checkMachineAST(ctx context.Context, value reflect.Value, depth int) error {
	if ctx.Err() != nil {
		return &MachineError{Code: "TIMEOUT"}
	}
	deny := func() error { return &MachineError{Code: "UNSUPPORTED_SQL"} }
	if depth > 512 {
		return deny()
	}
	if !value.IsValid() {
		return nil
	}
	if (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) && value.IsNil() {
		return nil
	}
	if value.CanInterface() {
		node := value.Interface()
		if statement, ok := node.(tree.Statement); ok {
			switch statement.(type) {
			case *tree.Select, *tree.SelectClause, *tree.ParenSelect, *tree.UnionClause, *tree.ValuesClause:
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
		switch node := node.(type) {
		case *tree.Select:
			if len(node.Locking) > 0 {
				return deny()
			}
		case *tree.CTE:
			if _, ok := node.Stmt.(*tree.Select); !ok {
				return deny()
			}
		case *tree.UnionClause:
			if node.Type != tree.UnionOp && node.Type != tree.IntersectOp && node.Type != tree.ExceptOp {
				return deny()
			}
		case *tree.AliasedTableExpr:
			if node.IndexFlags != nil {
				return deny()
			}
		}
	}
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		return checkMachineAST(ctx, value.Elem(), depth+1)
	case reflect.Slice, reflect.Array:
		for i := 0; i < value.Len(); i++ {
			if err := checkMachineAST(ctx, value.Index(i), depth+1); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			if value.Type().Field(i).PkgPath == "" {
				if err := checkMachineAST(ctx, value.Field(i), depth+1); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (c *MachineClient) QueryReadOnly(ctx context.Context, request MachineQuery) (MachineQueryResult, error) {
	result := MachineQueryResult{Columns: []MachineColumn{}, Rows: [][]*string{}}
	if c.access != models.CLIAccessRowRead {
		return result, &MachineError{Code: "POLICY_DENIED"}
	}
	if err := ValidateMachineQuery(ctx, request.SQL); err != nil {
		return result, err
	}
	if request.MaxRows == 0 {
		request.MaxRows = 100
	}
	if request.MaxRows < 1 || request.MaxRows > 1000 || len(request.Parameters) > 256 {
		return result, &MachineError{Code: "INVALID_INPUT"}
	}
	parameterBytes := 0
	for _, parameter := range request.Parameters {
		parameterBytes += len(parameter)
		if parameterBytes > 1<<20 || !utf8.Valid(parameter) {
			return result, &MachineError{Code: "INVALID_INPUT"}
		}
	}
	if !utf8.ValidString(request.Schema) {
		return result, &MachineError{Code: "INVALID_INPUT"}
	}
	err := c.withReadOnly(ctx, false, request.Schema, func(ctx context.Context) error {
		reader := c.conn.PgConn().ExecParams(ctx, request.SQL, request.Parameters, nil, nil, nil)
		fail := func(err error) error { c.Abort(); _, _ = reader.Close(); return err }
		budget := 2
		for _, field := range reader.FieldDescriptions() {
			if len(field.Name) > 64<<10 || !utf8.ValidString(field.Name) {
				return fail(&MachineError{Code: "RESOURCE_FAILURE"})
			}
			column := MachineColumn{Name: field.Name, TypeOID: field.DataTypeOID, Encoding: "postgres-text"}
			encoded, _ := json.Marshal(column)
			budget += len(encoded) + 1
			if budget > 1<<20 {
				return fail(&MachineError{Code: "RESOURCE_FAILURE"})
			}
			result.Columns = append(result.Columns, column)
		}
		for reader.NextRow() {
			values := reader.Values()
			if len(values) != len(result.Columns) {
				return fail(&MachineError{Code: "RESOURCE_FAILURE"})
			}
			for _, value := range values {
				if len(value) > 64<<10 || !utf8.Valid(value) {
					return fail(&MachineError{Code: "RESOURCE_FAILURE"})
				}
			}
			if len(result.Rows) >= request.MaxRows {
				result.Truncated = true
				continue
			}
			row := make([]*string, len(values))
			budget += 2
			for i, value := range values {
				budget++
				if value == nil {
					budget += 4
					continue
				}
				text := string(value)
				encoded, _ := json.Marshal(text)
				budget += len(encoded)
				if budget > 1<<20 {
					return fail(&MachineError{Code: "RESOURCE_FAILURE"})
				}
				row[i] = &text
			}
			if budget > 1<<20 {
				return fail(&MachineError{Code: "RESOURCE_FAILURE"})
			}
			result.Rows = append(result.Rows, row)
		}
		_, err := reader.Close()
		return err
	})
	if err != nil {
		return MachineQueryResult{}, err
	}
	return result, nil
}
