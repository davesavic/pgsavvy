package context

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// This file is a faithful 1D mirror of the grid substring matcher in
// pkg/gui/grid/search.go. The grid helpers are
// package-private to grid, so they are replicated here with rail-prefixed
// names (to avoid any future redeclaration inside package context) and the
// column dimension stripped: a rail row carries a single name string.
//
// Regression credit: any change to the smart-case / fold semantics here
// must mirror pkg/gui/grid/search.go's queryIsCaseSensitive,
// substringMatches, literalMatches, foldedMatches, foldedMatchAt.

// railQueryIsCaseSensitive applies smart-case: the search is
// case-sensitive iff the query contains at least one uppercase rune
// (vim/ripgrep). Mirrors grid/search.go:queryIsCaseSensitive.
func railQueryIsCaseSensitive(query string) bool {
	for _, r := range query {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

// railSubstringMatches returns every [start,end) byte range in orig where
// query matches, under the case mode. Offsets are valid in orig (the
// original name string), NOT in any lowercased copy. Mirrors
// grid/search.go:substringMatches.
func railSubstringMatches(orig, query string, caseSensitive bool) [][2]int {
	if query == "" {
		return nil
	}
	if caseSensitive {
		return railLiteralMatches(orig, query)
	}
	return railFoldedMatches(orig, strings.ToLower(query))
}

// railLiteralMatches finds non-overlapping byte ranges of query in orig
// via strings.Index. Mirrors grid/search.go:literalMatches.
func railLiteralMatches(orig, query string) [][2]int {
	var out [][2]int
	from := 0
	for {
		idx := strings.Index(orig[from:], query)
		if idx < 0 {
			return out
		}
		start := from + idx
		end := start + len(query)
		out = append(out, [2]int{start, end})
		from = end
	}
}

// railFoldedMatches finds non-overlapping ranges in orig that match
// lowerQuery (already lowercased) under case folding, returning byte
// offsets into orig. Mirrors grid/search.go:foldedMatches.
func railFoldedMatches(orig, lowerQuery string) [][2]int {
	var out [][2]int
	for i := 0; i < len(orig); {
		end, ok := railFoldedMatchAt(orig, i, lowerQuery)
		if !ok {
			_, size := utf8.DecodeRuneInString(orig[i:])
			i += size
			continue
		}
		out = append(out, [2]int{i, end})
		i = end
	}
	return out
}

// railFoldedMatchAt reports whether lowerQuery matches orig starting at
// byte offset start (under case folding), returning the exclusive end byte
// offset in orig when it does. Mirrors grid/search.go:foldedMatchAt.
func railFoldedMatchAt(orig string, start int, lowerQuery string) (int, bool) {
	q := lowerQuery
	pos := start
	for len(q) > 0 {
		if pos >= len(orig) {
			return 0, false
		}
		r, size := utf8.DecodeRuneInString(orig[pos:])
		folded := string(unicode.ToLower(r))
		if !strings.HasPrefix(q, folded) {
			return 0, false
		}
		q = q[len(folded):]
		pos += size
	}
	return pos, true
}

// railFirstMatchAtOrAfter returns the index of the first match whose
// RowIndex is at or after from, or 0 when no such match exists (so
// navigation starts from the top). matches is assumed in ascending
// RowIndex order. Mirrors grid/search.go:firstMatchAtOrAfter (1D).
func railFirstMatchAtOrAfter(matches []RailMatch, from int) int {
	for i, m := range matches {
		if m.RowIndex >= from {
			return i
		}
	}
	return 0
}
