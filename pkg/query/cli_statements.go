package query

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	ErrInvalidCLIInput   = errors.New("query: invalid CLI input")
	ErrUnsupportedCLISQL = errors.New("query: unsupported CLI SQL")
)

// CLIStatement is a half-open original byte range, excluding its separator.
// Leading/trailing whitespace and comments are retained, not normalized.
type CLIStatement struct {
	Start, End int
	Kind       StatementKind
}

type CLIStatementAnalysis struct {
	Statements             []CLIStatement
	WritesApprovalRequired bool
	DDLApprovalRequired    bool
}

// AnalyzeCLIStatements analyzes but never rewrites or executes SQL. Its lexical
// contract is PostgreSQL 17/18 with standard_conforming_strings=on and UTF-8.
// Approval deliberately retains EffectiveKind's heuristic, including its gaps.
func AnalyzeCLIStatements(ctx context.Context, sql string, parameterCount int, confirmWrites, confirmDDL bool) (CLIStatementAnalysis, error) {
	var result CLIStatementAnalysis
	if err := ctx.Err(); err != nil {
		return CLIStatementAnalysis{}, err
	}
	if len(sql) > 1048576 || parameterCount < 0 || !utf8.ValidString(sql) || strings.IndexByte(sql, 0) >= 0 {
		return CLIStatementAnalysis{}, ErrInvalidCLIInput
	}
	lexer := cliLexer{ctx: ctx, sql: sql}
	start, parens, bodies := 0, 0, 0
	hasToken := false
	var statement cliStatementContext
	finish := func(end int) {
		if hasToken {
			kind := EffectiveKind(sql[start:end])
			result.Statements = append(result.Statements, CLIStatement{Start: start, End: end, Kind: kind})
			result.WritesApprovalRequired = result.WritesApprovalRequired || confirmWrites && kind == KindDML
			result.DDLApprovalRequired = result.DDLApprovalRequired || confirmDDL && kind == KindDDL
		}
		start, hasToken = end+1, false
	}
	for {
		token, err := lexer.next()
		if err != nil {
			return CLIStatementAnalysis{}, err
		}
		if token.end == token.start {
			break
		}
		if token.word == ";" {
			// CREATE RULE can contain semicolon-separated actions in parentheses.
			if parens != 0 {
				continue
			}
			if bodies == 0 {
				finish(token.start)
			}
			statement = cliStatementContext{}
			continue
		}
		hasToken = true
		// gram.y requires every routine_body_stmt to end with ';', and stmt
		// excludes legacy transaction END. A body END is between elements,
		// never a CASE terminator or a label within a statement.
		if bodies > 0 && parens == 0 && statement.count == 0 && token.word == "END" {
			bodies--
			statement.count = 1
			continue
		}
		opened, err := statement.consume(token, parens)
		if err != nil {
			return CLIStatementAnalysis{}, err
		}
		if opened {
			bodies++
			statement = cliStatementContext{}
		}
		switch token.word {
		case "(":
			parens++
		case ")":
			parens--
			if parens < 0 {
				return CLIStatementAnalysis{}, ErrUnsupportedCLISQL
			}
		}
	}
	if parens != 0 || bodies != 0 {
		return CLIStatementAnalysis{}, ErrUnsupportedCLISQL
	}
	finish(len(sql))
	if err := ctx.Err(); err != nil {
		return CLIStatementAnalysis{}, err
	}
	if len(result.Statements) == 0 {
		return CLIStatementAnalysis{}, ErrInvalidCLIInput
	}
	if parameterCount > 0 && len(result.Statements) > 1 {
		return CLIStatementAnalysis{}, ErrUnsupportedCLISQL
	}
	return result, nil
}

type cliToken struct {
	start, end int
	word       string
	identifier bool
}

type cliStatementContext struct {
	prefix                [4]string
	count                 int
	routine, pendingBegin bool
	copy                  cliCopyPrefix
}

func (s *cliStatementContext) consume(token cliToken, parens int) (bool, error) {
	if s.count == 0 && token.word == "COPY" {
		s.copy.state = cliCopyStart
	}
	if s.count < len(s.prefix) {
		s.prefix[s.count] = token.word
		s.count++
		s.routine = s.routine || s.count == 2 && s.prefix[0] == "CREATE" && (s.prefix[1] == "FUNCTION" || s.prefix[1] == "PROCEDURE") ||
			s.count == 4 && s.prefix[0] == "CREATE" && s.prefix[1] == "OR" && s.prefix[2] == "REPLACE" && (s.prefix[3] == "FUNCTION" || s.prefix[3] == "PROCEDURE")
	}
	if s.copy.state != 0 && !(s.count == 1 && token.word == "COPY") {
		if err := s.copy.consume(token, parens); err != nil {
			return false, err
		}
	}
	opened := s.routine && parens == 0 && s.pendingBegin && token.word == "ATOMIC"
	// Names, argument types/defaults and RETURNS TABLE fields can themselves
	// contain BEGIN and ATOMIC; signature parentheses are not routine bodies.
	s.pendingBegin = s.routine && parens == 0 && token.word == "BEGIN"
	return opened, nil
}

const (
	cliCopyStart = iota + 1
	cliCopyName
	cliCopyAfterName
	cliCopyNameEscape
	cliCopyGroup
	cliCopyDirection
	cliCopyEndpoint
	cliCopyDone
)

type cliCopyPrefix struct{ state, depth int }

// CopyStmt is COPY [BINARY] qualified_name [(columns)] FROM/TO [PROGRAM]
// copy_file_name, or COPY (PreparableStmt) TO [PROGRAM] copy_file_name.
// An attr_name after '.' is ColLabel, which includes FROM and TO themselves.
func (c *cliCopyPrefix) consume(token cliToken, parens int) error {
	switch c.state {
	case cliCopyStart:
		if token.word == "BINARY" {
			c.state = cliCopyName
			return nil
		}
		if token.word == "(" {
			c.state, c.depth = cliCopyGroup, parens
			return nil
		}
		fallthrough
	case cliCopyName:
		if !token.identifier {
			return ErrUnsupportedCLISQL
		}
		c.state = cliCopyAfterName
	case cliCopyAfterName:
		switch token.word {
		case "UESCAPE":
			// base_yylex folds UIDENT [UESCAPE SCONST] into one IDENT.
			c.state = cliCopyNameEscape
		case ".":
			c.state = cliCopyName
		case "(":
			c.state, c.depth = cliCopyGroup, parens
		case "FROM", "TO":
			c.state = cliCopyEndpoint
		default:
			return ErrUnsupportedCLISQL
		}
	case cliCopyNameEscape:
		if token.identifier || token.word != "" {
			return ErrUnsupportedCLISQL
		}
		c.state = cliCopyAfterName
	case cliCopyGroup:
		if token.word == ")" && parens == c.depth+1 {
			c.state = cliCopyDirection
		}
	case cliCopyDirection:
		if token.word != "FROM" && token.word != "TO" {
			return ErrUnsupportedCLISQL
		}
		c.state = cliCopyEndpoint
	case cliCopyEndpoint:
		// Both spellings are accepted in either direction by copy_file_name.
		if token.word == "STDIN" || token.word == "STDOUT" {
			return ErrUnsupportedCLISQL
		}
		c.state = cliCopyDone
	}
	return nil
}

type cliLexer struct {
	ctx               context.Context
	sql               string
	pos               int
	operatorSuffixEnd int
}

func cliSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f' || b == '\v'
}
func cliIdentStart(b byte) bool {
	return b >= 0x80 || b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}
func cliIdentContinue(b byte) bool { return cliIdentStart(b) || b >= '0' && b <= '9' || b == '$' }

var cliNumber = func() *regexp.Regexp {
	const decimal = `[0-9](_?[0-9])*`
	const numeric = `(` + decimal + `\.(` + decimal + `)?|\.` + decimal + `)`
	const real = `(` + decimal + `|` + numeric + `)[Ee][+-]?` + decimal
	re := regexp.MustCompile(`^(` + decimal + `|0[xX](_?[0-9a-fA-F])+|0[oO](_?[0-7])+|0[bB](_?[01])+|` + numeric + `|` + real + `)`)
	re.Longest() // flex uses the longest match, not alternation order.
	return re
}()

// The delimiters and continuation rules follow PostgreSQL src/backend/parser/
// scan.l (REL_17_STABLE and REL_18_STABLE), not the TUI statement splitter.
func (l *cliLexer) next() (cliToken, error) {
	s := l.sql
	for l.pos < len(s) {
		if err := l.ctx.Err(); err != nil {
			return cliToken{}, err
		}
		i := l.pos
		switch {
		case cliSpace(s[i]):
			l.pos++
		case strings.HasPrefix(s[i:], "--"):
			l.pos += 2
			for l.pos < len(s) && s[l.pos] != '\n' && s[l.pos] != '\r' {
				l.pos++
			}
		case strings.HasPrefix(s[i:], "/*"):
			l.pos += 2
			depth := 1
			for l.pos < len(s) && depth > 0 {
				if l.pos&4095 == 0 {
					if err := l.ctx.Err(); err != nil {
						return cliToken{}, err
					}
				}
				switch {
				case strings.HasPrefix(s[l.pos:], "/*"):
					depth++
					l.pos += 2
				case strings.HasPrefix(s[l.pos:], "*/"):
					depth--
					l.pos += 2
				default:
					l.pos++
				}
			}
			if depth != 0 {
				return cliToken{}, ErrUnsupportedCLISQL
			}
		default:
			return l.token()
		}
	}
	return cliToken{start: l.pos, end: l.pos}, nil
}

func (l *cliLexer) token() (cliToken, error) {
	s, start := l.sql, l.pos
	token := cliToken{start: start}
	if start < l.operatorSuffixEnd {
		// PostgreSQL emits each trimmed trailing sign as a one-byte token.
		l.pos++
		token.end = l.pos
		return token, nil
	}
	i := start
	quote, escapes, doubled := byte(0), false, true
	switch {
	case s[i] == '\'' || s[i] == '"':
		quote = s[i]
	case i+1 < len(s) && s[i+1] == '\'' && strings.ContainsRune("eEbBxXnN", rune(s[i])):
		escapes = s[i] == 'e' || s[i] == 'E'
		doubled = !strings.ContainsRune("bBxX", rune(s[i]))
		i++
		quote = '\''
	case i+2 < len(s) && (s[i] == 'u' || s[i] == 'U') && s[i+1] == '&' && (s[i+2] == '\'' || s[i+2] == '"'):
		i += 2
		quote = s[i]
	}
	if quote != 0 {
		end, err := l.quoted(i, quote, escapes, doubled)
		if err != nil {
			return cliToken{}, err
		}
		l.pos, token.end = end, end
		token.identifier = quote == '"'
		return token, nil
	}
	if s[i] >= '0' && s[i] <= '9' || s[i] == '.' && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9' {
		end := i + len(cliNumber.FindString(s[i:]))
		if end < len(s) && s[end-1] == '.' && s[end] == '.' {
			end--
		}
		if end < len(s) && cliIdentStart(s[end]) {
			return cliToken{}, ErrUnsupportedCLISQL
		}
		l.pos, token.end = end, end
		return token, nil
	}
	if s[i] == '$' {
		if i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9' {
			end := i + 2
			for end < len(s) && s[end] >= '0' && s[end] <= '9' {
				end++
			}
			if end < len(s) && cliIdentStart(s[end]) {
				return cliToken{}, ErrUnsupportedCLISQL
			}
			number := strings.TrimLeft(s[i+1:end], "0")
			if len(number) > 10 || len(number) == 10 && number > "2147483647" {
				return cliToken{}, ErrUnsupportedCLISQL
			}
			l.pos, token.end = end, end
			return token, nil
		}
		j := i + 1
		if j < len(s) && cliIdentStart(s[j]) {
			j++
			for j < len(s) && cliIdentContinue(s[j]) && s[j] != '$' {
				j++
			}
		}
		if j < len(s) && s[j] == '$' {
			delimiter := s[i : j+1]
			end := strings.Index(s[j+1:], delimiter)
			if end < 0 {
				return cliToken{}, ErrUnsupportedCLISQL
			}
			l.pos = j + 1 + end + len(delimiter)
			token.end = l.pos
			return token, nil
		}
	}
	if strings.ContainsRune("~!@#^&|`?+-*/%<>=", rune(s[i])) {
		end := i + 1
		nonSQL := strings.ContainsRune("~!@#^&|`?%", rune(s[i]))
		for end < len(s) && strings.ContainsRune("~!@#^&|`?+-*/%<>=", rune(s[end])) {
			if end&4095 == 0 {
				if err := l.ctx.Err(); err != nil {
					return cliToken{}, err
				}
			}
			if strings.HasPrefix(s[end:], "/*") || strings.HasPrefix(s[end:], "--") {
				break
			}
			nonSQL = nonSQL || strings.ContainsRune("~!@#^&|`?%", rune(s[end]))
			end++
		}
		runEnd := end
		if !nonSQL {
			for end > i+1 && (s[end-1] == '+' || s[end-1] == '-') {
				if end&4095 == 0 {
					if err := l.ctx.Err(); err != nil {
						return cliToken{}, err
					}
				}
				end--
			}
		}
		if end-i >= 64 {
			return cliToken{}, ErrUnsupportedCLISQL
		}
		// The suffix contains only signs and no comment start; caching avoids
		// rescanning it for every subsequent sign (quadratic on unary runs).
		if end < runEnd {
			l.operatorSuffixEnd = runEnd
		}
		l.pos, token.end = end, end
		return token, nil
	}
	if cliIdentStart(s[i]) {
		token.identifier = true
		ascii := s[i] < 0x80
		i++
		for i < len(s) && cliIdentContinue(s[i]) {
			ascii = ascii && s[i] < 0x80
			i++
		}
		// PostgreSQL keywords use ASCII folding, not Unicode case folding.
		if ascii {
			token.word = strings.ToUpper(s[start:i])
		}
	} else {
		token.word = s[i : i+1]
		i++
	}
	l.pos, token.end = i, i
	return token, nil
}

func (l *cliLexer) quoted(start int, quote byte, escapes, doubled bool) (int, error) {
	s, i := l.sql, start+1
	var decoded []byte
	for i < len(s) {
		if i&4095 == 0 {
			if err := l.ctx.Err(); err != nil {
				return 0, err
			}
		}
		if s[i] == quote {
			if doubled && i+1 < len(s) && s[i+1] == quote {
				if escapes {
					decoded = append(decoded, quote)
				}
				i += 2
				continue
			}
			if quote == '"' && i == start+1 {
				return 0, ErrUnsupportedCLISQL
			}
			i++
			if quote == '\'' {
				if next := cliQuoteContinuation(s, i); next >= 0 {
					i = next + 1
					continue
				}
			}
			if escapes && (!utf8.Valid(decoded) || strings.IndexByte(string(decoded), 0) >= 0) {
				return 0, ErrUnsupportedCLISQL
			}
			return i, nil
		}
		if escapes && s[i] == '\\' {
			var err error
			i, decoded, err = cliEscape(s, i, decoded)
			if err != nil {
				return 0, err
			}
		} else {
			if escapes {
				decoded = append(decoded, s[i])
			}
			i++
		}
	}
	return 0, ErrUnsupportedCLISQL
}

// A newline is required; line comments count, block comments do not. Continued
// E strings retain escape mode even though the next quote has no E prefix.
func cliQuoteContinuation(s string, i int) int {
	newline := false
	for i < len(s) {
		if cliSpace(s[i]) {
			newline = newline || s[i] == '\r' || s[i] == '\n'
			i++
		} else if strings.HasPrefix(s[i:], "--") {
			i += 2
			for i < len(s) && s[i] != '\r' && s[i] != '\n' {
				i++
			}
		} else {
			break
		}
	}
	if newline && i < len(s) && s[i] == '\'' {
		return i
	}
	return -1
}

func cliHex(b byte) int {
	switch {
	case b >= '0' && b <= '9':
		return int(b - '0')
	case b >= 'a' && b <= 'f':
		return int(b-'a') + 10
	case b >= 'A' && b <= 'F':
		return int(b-'A') + 10
	}
	return -1
}

func cliUnicodeEscape(s string, i int) (int, rune, error) {
	if i+1 >= len(s) || s[i] != '\\' || (s[i+1] != 'u' && s[i+1] != 'U') {
		return 0, 0, ErrUnsupportedCLISQL
	}
	digits := 4
	if s[i+1] == 'U' {
		digits = 8
	}
	i += 2
	var value uint32
	for range digits {
		if i >= len(s) || cliHex(s[i]) < 0 {
			return 0, 0, ErrUnsupportedCLISQL
		}
		value = value*16 + uint32(cliHex(s[i]))
		i++
	}
	if value == 0 || value > utf8.MaxRune {
		return 0, 0, ErrUnsupportedCLISQL
	}
	return i, rune(value), nil
}

func cliEscape(s string, i int, decoded []byte) (int, []byte, error) {
	i++
	if i >= len(s) {
		return 0, nil, ErrUnsupportedCLISQL
	}
	c := s[i]
	if c == 'u' || c == 'U' {
		next, value, err := cliUnicodeEscape(s, i-1)
		if err != nil {
			return 0, nil, err
		}
		if value >= 0xD800 && value <= 0xDBFF {
			var second rune
			next, second, err = cliUnicodeEscape(s, next)
			if err != nil || second < 0xDC00 || second > 0xDFFF {
				return 0, nil, ErrUnsupportedCLISQL
			}
			value = 0x10000 + (value-0xD800)*0x400 + second - 0xDC00
		} else if value >= 0xDC00 && value <= 0xDFFF {
			return 0, nil, ErrUnsupportedCLISQL
		}
		return next, utf8.AppendRune(decoded, value), nil
	}
	if c >= '0' && c <= '7' {
		value, count := byte(0), 0
		for i < len(s) && count < 3 && s[i] >= '0' && s[i] <= '7' {
			value = value*8 + s[i] - '0'
			i++
			count++
		}
		return i, append(decoded, value), nil
	}
	if c == 'x' && i+1 < len(s) && cliHex(s[i+1]) >= 0 {
		i++
		value, count := byte(0), 0
		for i < len(s) && count < 2 && cliHex(s[i]) >= 0 {
			value = value*16 + byte(cliHex(s[i]))
			i++
			count++
		}
		return i, append(decoded, value), nil
	}
	switch c {
	case 'b':
		c = '\b'
	case 'f':
		c = '\f'
	case 'n':
		c = '\n'
	case 'r':
		c = '\r'
	case 't':
		c = '\t'
	}
	return i + 1, append(decoded, c), nil
}
