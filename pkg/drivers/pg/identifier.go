package pg

import "strings"

// QuoteIdent double-quote-wraps a SQL identifier and doubles any embedded `"`,
// matching PostgreSQL's quoting rules for identifiers that may contain
// reserved words, mixed case, dots, or other special characters. The empty
// string is returned as `""` (the SQL representation of an empty identifier),
// preserving round-trippability through QuoteQualified.
//
// ADR-21: shared helper consumed by F2 (this producer), and the A5/B5/B6
// consumers when they assemble UPDATE / DELETE / INSERT statements against
// editable result sets.
func QuoteIdent(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' {
			b.WriteString(`""`)
			continue
		}
		b.WriteByte(c)
	}
	b.WriteByte('"')
	return b.String()
}

// QuoteQualified joins a schema and relation name into a `"schema"."name"`
// reference. When schema is empty, the leading qualifier is omitted and the
// result is just `QuoteIdent(name)` — callers rely on this to emit an
// unqualified identifier when introspection didn't resolve a schema.
func QuoteQualified(schema, name string) string {
	if schema == "" {
		return QuoteIdent(name)
	}
	return QuoteIdent(schema) + "." + QuoteIdent(name)
}
