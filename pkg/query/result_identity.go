package query

import (
	"strings"
	"unicode"
)

// ResultIdentity describes whether a query's result set has a stable
// 1:1 mapping back to rows in a single base table — the prerequisite
// for features like editable cells, per-table hidden-column persistence
// (T6), and SQL-INSERTs export (T9).
//
// DetectFromQuery is conservative: it returns HasRowIdentity=true only
// when the SQL is a single SELECT against a single base table with no
// joins, CTEs, subqueries, set operations, aggregates, or table-valued
// functions. When in doubt, it returns the zero value.
type ResultIdentity struct {
	// BaseTable is the qualified table name in the form "<schema>.<table>"
	// when a schema qualifier is present, or just "<table>" otherwise.
	// Unquoted identifiers are lowercased; quoted identifiers preserve
	// their inner casing and may legally contain dots (e.g. "weird.name").
	BaseTable string

	// HasRowIdentity is true when the query is judged to have a stable
	// row identity backed by BaseTable.
	HasRowIdentity bool

	// Editable, RowIdentity, and DisabledReason are populated by callers
	// AFTER pg_class+pg_index introspection runs (see
	// pkg/drivers/pg.EditabilityIntrospect). DetectFromQuery leaves them
	// zero — SQL parsing alone cannot distinguish a base table from a
	// view, materialised view, or partition parent.
	Editable       bool
	RowIdentity    []int
	DisabledReason string
}

// DetectFromQuery runs the heuristic over sql and returns a
// ResultIdentity. The detector accepts only this shape:
//
//	SELECT ( * | <col-list> ) FROM (<schema>.)?<table>
//	  ( WHERE … | ORDER BY … | LIMIT … | ; | EOF )
//
// Multi-statement input is supported: if any statement is rejected,
// the whole result is the zero value. Comments are stripped before
// tokenization, but the stripper respects string literals so a
// payload like `WHERE name='a--b'` is preserved verbatim.
func DetectFromQuery(sql string) ResultIdentity {
	cleaned := stripComments(sql)
	stmts := splitStatementsRespectingStrings(cleaned)
	if len(stmts) == 0 {
		return ResultIdentity{}
	}

	var first ResultIdentity
	for i, stmt := range stmts {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		id, ok := detectSingleStatement(stmt)
		if !ok {
			return ResultIdentity{}
		}
		if i == 0 {
			first = id
		}
	}
	return first
}

// stripComments removes SQL line comments (`-- …\n`) and block
// comments (`/* … */`) from src, but only when they appear outside of
// single- or double-quoted string regions. Quoted regions are emitted
// verbatim — this is the AC the AMENDMENTS section calls out
// explicitly: `WHERE name='a--b'` must NOT be truncated.
//
// Block comments are not nested; a `*/` ends the first `/*` regardless
// of inner `/*` runs. This matches the PostgreSQL grammar's
// non-nesting variant which is sufficient for the heuristic.
func stripComments(src string) string {
	var b strings.Builder
	b.Grow(len(src))
	i := 0
	for i < len(src) {
		c := src[i]
		switch c {
		case '\'', '"':
			// Copy the quoted segment verbatim. Doubled quotes
			// (e.g. '' or "") inside the literal are SQL's escape
			// for the quote itself; we re-enter the loop on the
			// second quote so the closing-quote detection stays
			// correct.
			quote := c
			b.WriteByte(c)
			i++
			for i < len(src) {
				if src[i] == quote {
					b.WriteByte(quote)
					i++
					if i < len(src) && src[i] == quote {
						// doubled — escape; stay inside
						b.WriteByte(quote)
						i++
						continue
					}
					break
				}
				b.WriteByte(src[i])
				i++
			}
		case '-':
			if i+1 < len(src) && src[i+1] == '-' {
				// Line comment — skip to end of line.
				i += 2
				for i < len(src) && src[i] != '\n' {
					i++
				}
				// Preserve the newline so line-based context
				// (e.g. ORDER BY on the next line) still separates.
				if i < len(src) {
					b.WriteByte('\n')
					i++
				}
			} else {
				b.WriteByte(c)
				i++
			}
		case '/':
			if i+1 < len(src) && src[i+1] == '*' {
				i += 2
				for i+1 < len(src) && (src[i] != '*' || src[i+1] != '/') {
					i++
				}
				if i+1 < len(src) {
					i += 2 // consume */
				} else {
					i = len(src)
				}
				// Emit a single space to keep tokens separated.
				b.WriteByte(' ')
			} else {
				b.WriteByte(c)
				i++
			}
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// splitStatementsRespectingStrings splits src on `;` boundaries that
// sit outside of quoted regions. Empty segments are dropped.
func splitStatementsRespectingStrings(src string) []string {
	var out []string
	start := 0
	i := 0
	for i < len(src) {
		c := src[i]
		switch c {
		case '\'', '"':
			quote := c
			i++
			for i < len(src) {
				if src[i] == quote {
					i++
					if i < len(src) && src[i] == quote {
						i++
						continue
					}
					break
				}
				i++
			}
		case ';':
			seg := src[start:i]
			if strings.TrimSpace(seg) != "" {
				out = append(out, seg)
			}
			i++
			start = i
		default:
			i++
		}
	}
	if start < len(src) {
		seg := src[start:]
		if strings.TrimSpace(seg) != "" {
			out = append(out, seg)
		}
	}
	return out
}

// token represents a single lexed unit. tIdent covers both unquoted
// identifiers and quoted identifiers — the raw field carries the
// original spelling (lowercased for unquoted, inner-text-preserved
// for quoted) and quoted records whether the source was double-quoted.
type token struct {
	kind    tokenKind
	raw     string
	quoted  bool
	literal bool // string literal (single-quoted)
}

type tokenKind int

const (
	tEOF tokenKind = iota
	tIdent
	tNumber
	tString
	tPunct
	tStar
	tComma
	tDot
	tLParen
	tRParen
)

// tokenize lexes stmt into a flat slice. Whitespace is dropped.
// Keywords are returned as tIdent — caller upper-cases when comparing.
func tokenize(stmt string) []token {
	var out []token
	i := 0
	for i < len(stmt) {
		c := stmt[i]
		switch {
		case unicode.IsSpace(rune(c)):
			i++
		case c == '\'':
			// String literal — content irrelevant for our purposes.
			i++
			var sb strings.Builder
			for i < len(stmt) {
				if stmt[i] == '\'' {
					i++
					if i < len(stmt) && stmt[i] == '\'' {
						sb.WriteByte('\'')
						i++
						continue
					}
					break
				}
				sb.WriteByte(stmt[i])
				i++
			}
			out = append(out, token{kind: tString, raw: sb.String(), literal: true})
		case c == '"':
			i++
			var sb strings.Builder
			for i < len(stmt) {
				if stmt[i] == '"' {
					i++
					if i < len(stmt) && stmt[i] == '"' {
						sb.WriteByte('"')
						i++
						continue
					}
					break
				}
				sb.WriteByte(stmt[i])
				i++
			}
			out = append(out, token{kind: tIdent, raw: sb.String(), quoted: true})
		case c == '*':
			out = append(out, token{kind: tStar, raw: "*"})
			i++
		case c == ',':
			out = append(out, token{kind: tComma, raw: ","})
			i++
		case c == '.':
			out = append(out, token{kind: tDot, raw: "."})
			i++
		case c == '(':
			out = append(out, token{kind: tLParen, raw: "("})
			i++
		case c == ')':
			out = append(out, token{kind: tRParen, raw: ")"})
			i++
		case isIdentStart(c):
			start := i
			for i < len(stmt) && isIdentPart(stmt[i]) {
				i++
			}
			out = append(out, token{kind: tIdent, raw: strings.ToLower(stmt[start:i])})
		case c >= '0' && c <= '9':
			start := i
			for i < len(stmt) && (stmt[i] == '.' || (stmt[i] >= '0' && stmt[i] <= '9')) {
				i++
			}
			out = append(out, token{kind: tNumber, raw: stmt[start:i]})
		default:
			// Unknown punctuation — punt; treat as opaque so the
			// stricter shape match below decides accept/reject.
			out = append(out, token{kind: tPunct, raw: string(c)})
			i++
		}
	}
	return out
}

func isIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9') || c == '$'
}

// rejected SQL keywords that, if seen anywhere in the statement,
// disqualify it from row-identity. The list is intentionally narrow:
// presence of any of these signals shape we don't support.
var rejectKeywords = map[string]struct{}{
	"join":      {},
	"union":     {},
	"intersect": {},
	"except":    {},
	"with":      {},
	"group":     {}, // GROUP BY
	"having":    {},
	"distinct":  {},
	"window":    {},
	"over":      {}, // window funcs in select-list
}

// aggregate functions that, when followed by `(`, reject the
// statement. Bare identifiers with the same name are fine — we only
// look for `agg(`.
var aggregateFuncs = map[string]struct{}{
	"count": {}, "sum": {}, "avg": {}, "min": {}, "max": {},
	"array_agg": {}, "string_agg": {}, "json_agg": {}, "jsonb_agg": {},
	"bool_and": {}, "bool_or": {}, "every": {},
}

// detectSingleStatement runs the heuristic on a single statement and
// returns (ResultIdentity, accepted).
func detectSingleStatement(stmt string) (ResultIdentity, bool) {
	toks := tokenize(stmt)
	if len(toks) == 0 {
		return ResultIdentity{}, false
	}

	// Must start with SELECT.
	if !isKeyword(toks[0], "select") {
		return ResultIdentity{}, false
	}

	// Reject if any banned keyword appears, or if any aggregate
	// function call is present in the statement.
	for i, t := range toks {
		if t.kind != tIdent || t.quoted {
			continue
		}
		if _, bad := rejectKeywords[t.raw]; bad {
			return ResultIdentity{}, false
		}
		if _, agg := aggregateFuncs[t.raw]; agg {
			if i+1 < len(toks) && toks[i+1].kind == tLParen {
				return ResultIdentity{}, false
			}
		}
	}

	// Locate top-level FROM. "Top-level" = not inside parens. The
	// presence of any `(` in the SELECT list before FROM is treated
	// as a subquery/function-call and rejected (conservative).
	fromIdx := -1
	depth := 0
	for i := 1; i < len(toks); i++ {
		t := toks[i]
		switch t.kind {
		case tLParen:
			depth++
		case tRParen:
			depth--
		case tIdent:
			if depth == 0 && !t.quoted && t.raw == "from" {
				fromIdx = i
			}
		}
		if fromIdx >= 0 {
			break
		}
	}
	if fromIdx < 0 {
		return ResultIdentity{}, false
	}
	if depth != 0 {
		return ResultIdentity{}, false
	}

	// Select list spans toks[1:fromIdx]. Reject if it contains a
	// `(` — that's a function call or subquery and we can't tell
	// whether it preserves row identity. (Aggregates are already
	// caught above, but plain functions like `lower(name)` also
	// drop us out of the accept set conservatively.)
	for _, t := range toks[1:fromIdx] {
		if t.kind == tLParen {
			return ResultIdentity{}, false
		}
	}

	// FROM clause: parse (schema.)?table — accept identifier, an
	// optional `.identifier`, then require the tail to be empty or
	// start with one of: WHERE, ORDER, LIMIT, OFFSET, FETCH, FOR.
	rest := toks[fromIdx+1:]
	if len(rest) == 0 {
		return ResultIdentity{}, false
	}

	schemaTok, tableTok, consumed, ok := parseQualifiedTable(rest)
	if !ok {
		return ResultIdentity{}, false
	}

	tail := skipAlias(rest[consumed:])
	if !validTail(tail) {
		return ResultIdentity{}, false
	}

	base := formatBaseTable(schemaTok, tableTok)
	if base == "" {
		return ResultIdentity{}, false
	}
	return ResultIdentity{BaseTable: base, HasRowIdentity: true}, true
}

// parseQualifiedTable expects rest[0] to be an identifier (quoted or
// unquoted). If rest[1] is a dot and rest[2] is another identifier,
// returns (schema, table, 3). Otherwise (zero, table, 1).
func parseQualifiedTable(rest []token) (schema, table token, consumed int, ok bool) {
	if len(rest) < 1 {
		return token{}, token{}, 0, false
	}
	if rest[0].kind != tIdent {
		return token{}, token{}, 0, false
	}
	// Reject if the bare-identifier name is a SQL keyword that
	// would change the shape (already filtered for the global list,
	// but FROM <keyword> is meaningless — guard anyway).
	if !rest[0].quoted && isReservedTableName(rest[0].raw) {
		return token{}, token{}, 0, false
	}
	if len(rest) >= 3 && rest[1].kind == tDot && rest[2].kind == tIdent {
		if !rest[2].quoted && isReservedTableName(rest[2].raw) {
			return token{}, token{}, 0, false
		}
		return rest[0], rest[2], 3, true
	}
	return token{}, rest[0], 1, true
}

func isReservedTableName(lower string) bool {
	switch lower {
	case "select", "from", "where", "order", "group", "having", "limit",
		"offset", "fetch", "for", "join", "on", "union", "intersect",
		"except", "with", "as":
		return true
	}
	return false
}

// skipAlias consumes an optional table alias from the start of the FROM
// tail — either `AS name` or a bare `name` — and returns the remaining
// tokens. A bare identifier is only treated as an alias when it is not a
// reserved clause keyword (so WHERE/ORDER/etc. fall through to validTail).
func skipAlias(tail []token) []token {
	if len(tail) == 0 || tail[0].kind != tIdent {
		return tail
	}
	if !tail[0].quoted && tail[0].raw == "as" {
		if len(tail) >= 2 && tail[1].kind == tIdent {
			return tail[2:]
		}
		return tail
	}
	if tail[0].quoted || !isReservedTableName(tail[0].raw) {
		return tail[1:]
	}
	return tail
}

// validTail returns true when the tokens after the FROM <table> form
// a recognised tail: empty, or starting with one of the allowed
// clause keywords.
func validTail(tail []token) bool {
	if len(tail) == 0 {
		return true
	}
	head := tail[0]
	if head.kind != tIdent || head.quoted {
		return false
	}
	switch head.raw {
	case "where", "order", "limit", "offset", "fetch", "for":
		return true
	default:
		return false
	}
}

// formatBaseTable assembles the BaseTable string per the casing rules:
// unquoted idents lowercase, quoted idents preserve inner casing
// (including dots). When both schema and table are present, joined
// with ".".
func formatBaseTable(schema, table token) string {
	tableStr := identString(table)
	if tableStr == "" {
		return ""
	}
	if schema.raw == "" && !schema.quoted {
		return tableStr
	}
	schemaStr := identString(schema)
	if schemaStr == "" {
		return tableStr
	}
	return schemaStr + "." + tableStr
}

func identString(t token) string {
	if t.quoted {
		return t.raw
	}
	// tokenize already lowercased unquoted identifiers.
	return t.raw
}

func isKeyword(t token, kw string) bool {
	return t.kind == tIdent && !t.quoted && t.raw == kw
}
