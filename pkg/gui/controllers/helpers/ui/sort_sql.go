package ui

import (
	"fmt"
	"strings"
)

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

	return fmt.Sprintf("SELECT * FROM (\n%s\n) _dbsavvy_sort\nORDER BY %d %s", stripped, ordinal1Based, keyword)
}
