package ui

import (
	"fmt"
	"regexp"
	"strings"
)

// Applied only to the byte-aligned lexical mask, never to raw SQL.
var trailingLimitOffsetRE = regexp.MustCompile(`(?i)\s+(LIMIT\s+\d+(?:\s+OFFSET\s+\d+)?|OFFSET\s+\d+(?:\s+LIMIT\s+\d+)?)\s*$`)

// sortDir is the authoritative per-Tab sort direction. It is intentionally
// local to this package (rather than reusing grid's display-only SortAsc/
// SortDesc) because the design moves authoritative sort state to the Tab.
type sortDir int

const (
	sortClear sortDir = iota
	sortAsc
	sortDesc
)

// wrapSorted wraps orig in an ORDER-BY-by-ordinal derived table so the result
// can be re-run sorted by the given 1-based column ordinal. When dir is
// sortClear the original query is returned verbatim (byte-for-byte, no wrap).
//
// The wrap never concatenates a column name: only the integer ordinal is used,
// which sidesteps ambiguous/duplicate column names (e.g. joins selecting two
// "id" columns). A newline is emitted unconditionally after orig and before the
// closing paren so a trailing line comment in orig cannot comment out ORDER BY.
//
// With known lexical boundaries, terminal semicolons can also be removed
// before trailing comments. Otherwise retain the conservative suffix strip.
func wrapSorted(orig string, ordinal1Based int, dir sortDir) string {
	if dir == sortClear {
		return orig
	}

	keyword := "ASC"
	if dir == sortDesc {
		keyword = "DESC"
	}

	stripped := strings.TrimRight(orig, " \t\n\r")
	code := sortSQLCode(stripped)

	// Hoist a trailing LIMIT/OFFSET out of the inner query and re-apply it after
	// the ORDER BY. If left inside the derived table, Postgres applies the LIMIT
	// to the unordered inner scan first, so the outer ORDER BY would sort only an
	// arbitrary subset of rows.
	var tail string
	if code != nil {
		text := []byte(stripped)
		for i := len(code) - 1; i >= 0; i-- {
			if strings.ContainsRune(" \t\n\r\f", rune(code[i])) {
				continue
			}
			if code[i] != ';' {
				break
			}
			text[i], code[i] = ' ', ' '
		}
		stripped = strings.TrimRight(string(text), " \t\n\r")
		code = code[:len(stripped)]
		if loc := trailingLimitOffsetRE.FindSubmatchIndex(code); loc != nil {
			tail = stripped[loc[2]:]
			stripped = strings.TrimRight(stripped[:loc[2]], " \t\n\r")
		}
	} else {
		stripped = strings.TrimRight(stripped, " \t\n\r;")
	}

	wrapped := fmt.Sprintf("SELECT * FROM (\n%s\n) _pgsavvy_sort\nORDER BY %d %s", stripped, ordinal1Based, keyword)
	if tail != "" {
		wrapped += "\n" + tail
	}
	return wrapped
}

// Comments become whitespace; literals and parenthesized SQL become barriers.
// Keep byte offsets and line endings intact: the display highlighter normalizes
// CRLF and does not recognize backslash-escaped quotes in PostgreSQL E strings.
// An incomplete lexical construct refuses clause relocation.
func sortSQLCode(sql string) []byte {
	code := []byte(sql)
	depth := 0
	for i := 0; i < len(sql); {
		start := i
		fill := byte(0)
		switch {
		case strings.HasPrefix(sql[i:], "--"):
			i += 2
			for i < len(sql) && sql[i] != '\n' && sql[i] != '\r' {
				i++
			}
			fill = ' '
		case strings.HasPrefix(sql[i:], "/*"):
			i += 2
			comments := 1
			for i < len(sql) && comments > 0 {
				switch {
				case strings.HasPrefix(sql[i:], "/*"):
					comments++
					i += 2
				case strings.HasPrefix(sql[i:], "*/"):
					comments--
					i += 2
				default:
					i++
				}
			}
			if comments != 0 {
				return nil
			}
			fill = ' '
		case sql[i] == '\'' || sql[i] == '"':
			quote := sql[i]
			escaped := quote == '\'' && i > 0 && (sql[i-1] == 'E' || sql[i-1] == 'e') && (i == 1 || !sortSQLIdentByte(sql[i-2]))
			i++
			closed := false
			for i < len(sql) {
				if quote == '\'' && !escaped && sql[i] == '\\' {
					end := i
					for end < len(sql) && sql[end] == '\\' {
						end++
					}
					// Only an odd run before a quote changes quote boundaries
					// with the unknown standard_conforming_strings setting.
					if end < len(sql) && sql[end] == quote && (end-i)%2 != 0 {
						return nil
					}
					i = end
					continue
				}
				if escaped && sql[i] == '\\' {
					i = min(i+2, len(sql))
				} else if sql[i] == quote {
					i++
					if i < len(sql) && sql[i] == quote {
						i++
						continue
					}
					if quote == '\'' {
						if next := sortStringContinuation(sql, i); next >= 0 {
							i = next
							continue
						}
					}
					closed = true
					break
				} else {
					i++
				}
			}
			if !closed {
				return nil
			}
			fill = '?'
		case sql[i] == '$' && (i == 0 || !sortSQLIdentByte(sql[i-1])):
			i++
			if i < len(sql) && (sql[i] < '0' || sql[i] > '9') {
				for i < len(sql) && sql[i] != '$' && sortSQLIdentByte(sql[i]) {
					i++
				}
				if i < len(sql) && sql[i] == '$' {
					i++
					delimiter := sql[start:i]
					end := strings.Index(sql[i:], delimiter)
					if end < 0 {
						return nil
					}
					i += end + len(delimiter)
					fill = '?'
				}
			}
		case sql[i] == '(':
			depth++
			i++
			fill = '?'
		case sql[i] == ')':
			depth--
			if depth < 0 {
				return nil
			}
			i++
			fill = '?'
		default:
			i++
		}
		if depth > 0 {
			fill = '?'
		}
		if fill != 0 {
			for j := start; j < i; j++ {
				code[j] = fill
			}
		}
	}
	if depth != 0 {
		return nil
	}
	return code
}

// A newline-separated continuation keeps the first segment's escape mode.
// PostgreSQL permits line comments in this gap, but not block comments.
func sortStringContinuation(sql string, i int) int {
	newline := false
	for i < len(sql) {
		switch sql[i] {
		case ' ', '\t', '\f', '\v':
			i++
		case '\n', '\r':
			newline = true
			i++
		case '-':
			if !strings.HasPrefix(sql[i:], "--") {
				return -1
			}
			i += 2
			for i < len(sql) && sql[i] != '\n' && sql[i] != '\r' {
				i++
			}
		case '\'':
			if newline {
				return i + 1
			}
			return -1
		default:
			return -1
		}
	}
	return -1
}

func sortSQLIdentByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b == '$' || b >= 0x80
}
