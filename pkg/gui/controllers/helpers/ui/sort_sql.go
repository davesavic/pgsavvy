package ui

import (
	"fmt"
	"regexp"
	"strings"
)

// trailingLimitOffsetRE matches a trailing LIMIT/OFFSET clause (integer
// literals, either order) at the very end of a statement. Anchored with $ and
// requiring leading whitespace so it only ever matches the statement's own
// outermost tail clause — never a LIMIT inside a string literal (which is
// followed by a closing quote, not end-of-string) or a subquery's LIMIT
// (followed by a closing paren). Group 1 is the clause text, preserved verbatim.
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
// The trailing-';' strip is suffix-only (TrimRight) so it is string-literal
// safe: a ';' inside a literal such as WHERE x='a;b' is never truncated.
func wrapSorted(orig string, ordinal1Based int, dir sortDir) string {
	if dir == sortClear {
		return orig
	}

	keyword := "ASC"
	if dir == sortDesc {
		keyword = "DESC"
	}

	stripped := strings.TrimRight(orig, " \t\n\r;")

	// Hoist a trailing LIMIT/OFFSET out of the inner query and re-apply it after
	// the ORDER BY. If left inside the derived table, Postgres applies the LIMIT
	// to the unordered inner scan first, so the outer ORDER BY would sort only an
	// arbitrary subset of rows.
	var tail string
	if loc := trailingLimitOffsetRE.FindStringSubmatchIndex(stripped); loc != nil {
		tail = stripped[loc[2]:loc[3]]
		stripped = stripped[:loc[0]]
	}

	wrapped := fmt.Sprintf("SELECT * FROM (\n%s\n) _dbsavvy_sort\nORDER BY %d %s", stripped, ordinal1Based, keyword)
	if tail != "" {
		wrapped += "\n" + tail
	}
	return wrapped
}
