package query

import "strings"

// StatementKind classifies a SQL statement by its leading keyword. It is a
// coarse, parser-free classification used to decide whether a statement
// needs a confirmation prompt before execution (dbsavvy-wxkf), not a full
// SQL grammar.
type StatementKind int

const (
	// KindOther covers read-only and miscellaneous statements (SELECT,
	// WITH, SHOW, EXPLAIN, SET, BEGIN, ...). These never trigger a
	// confirmation prompt.
	KindOther StatementKind = iota
	// KindDML is a data-mutating statement (INSERT, UPDATE, DELETE, MERGE).
	KindDML
	// KindDDL is a schema/permission-mutating statement (CREATE, ALTER,
	// DROP, TRUNCATE, COMMENT, GRANT, REVOKE).
	KindDDL
)

// dmlKeywords / ddlKeywords map a leading keyword to its kind. A
// writable CTE (WITH ... UPDATE ...) is NOT detected — it leads with WITH
// and classifies as KindOther. Confirmation for those is a known gap.
var (
	dmlKeywords = map[string]struct{}{
		"INSERT": {}, "UPDATE": {}, "DELETE": {}, "MERGE": {},
	}
	ddlKeywords = map[string]struct{}{
		"CREATE": {}, "ALTER": {}, "DROP": {}, "TRUNCATE": {},
		"COMMENT": {}, "GRANT": {}, "REVOKE": {},
	}
)

// Classify returns the StatementKind of sql based on its first keyword,
// skipping leading whitespace, line comments (-- … EOL), and block
// comments (/* … */).
func Classify(sql string) StatementKind {
	kw := leadingKeyword(sql)
	if _, ok := dmlKeywords[kw]; ok {
		return KindDML
	}
	if _, ok := ddlKeywords[kw]; ok {
		return KindDDL
	}
	return KindOther
}

// leadingKeyword extracts and upper-cases the first SQL keyword, after
// stripping leading whitespace and comments. Returns "" when none remains.
func leadingKeyword(sql string) string {
	s := skipLeading(sql)
	end := 0
	for end < len(s) && isWordByte(s[end]) {
		end++
	}
	return strings.ToUpper(s[:end])
}

// skipLeading drops leading whitespace and SQL comments, returning the
// remainder starting at the first significant byte.
func skipLeading(s string) string {
	for {
		s = strings.TrimLeft(s, " \t\r\n\f\v")
		if strings.HasPrefix(s, "--") {
			if i := strings.IndexByte(s, '\n'); i >= 0 {
				s = s[i+1:]
				continue
			}
			return ""
		}
		if strings.HasPrefix(s, "/*") {
			if i := strings.Index(s[2:], "*/"); i >= 0 {
				s = s[2+i+2:]
				continue
			}
			return ""
		}
		return s
	}
}

func isWordByte(b byte) bool {
	return b == '_' ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9')
}
