package grid

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// twoColView builds a view with two text columns and the supplied rows
// installed. Cursor starts at (0,0); column meta is text/text.
func twoColView(t *testing.T, rows [][]any) *View {
	t.Helper()
	v := NewView()
	v.SetColumns([]models.ColumnMeta{
		{Name: "name", TypeName: "text"},
		{Name: "city", TypeName: "text"},
	})
	for _, r := range rows {
		v.AppendRows([]models.Row{{Values: r}})
	}
	return v
}

// TestSetSearch_SmartCaseInsensitive pins: an all-lowercase query matches
// case-insensitively (vim/ripgrep smart-case).
func TestSetSearch_SmartCaseInsensitive(t *testing.T) {
	v := twoColView(t, [][]any{
		{"Alice", "NY"},
		{"bob", "sf"},
	})
	v.SetSearch("a")
	_, _, total, active := v.SearchStatus()
	require.True(t, active)
	// "a" matches "Alice" (A folded) and... "NY"? no. "bob"? no. "sf"? no.
	// Only Alice's 'A'. So 1 match.
	require.Equal(t, 1, total, "lowercase query is case-insensitive: matches 'A' in Alice")
}

// TestSetSearch_SmartCaseSensitiveWhenUppercase pins: a query with an
// uppercase rune becomes case-sensitive.
func TestSetSearch_SmartCaseSensitiveWhenUppercase(t *testing.T) {
	v := twoColView(t, [][]any{
		{"Alice", "alice"},
	})
	v.SetSearch("A")
	_, _, total, _ := v.SearchStatus()
	require.Equal(t, 1, total, "uppercase query is case-sensitive: matches only the capital A")
}

// TestSetSearch_AllColumns pins: the match list spans all columns, not just
// the cursor column.
func TestSetSearch_AllColumns(t *testing.T) {
	v := twoColView(t, [][]any{
		{"foo", "bar"},
		{"baz", "foo"},
	})
	v.SetSearch("foo")
	_, _, total, _ := v.SearchStatus()
	require.Equal(t, 2, total, "foo appears in col 0 row 0 and col 1 row 1")
}

// TestSetSearch_CellMajorReadingOrder pins: matches are ordered by
// (row asc, col asc) and currentMatchIdx starts at/after the cursor.
func TestSetSearch_CellMajorReadingOrder(t *testing.T) {
	v := twoColView(t, [][]any{
		{"x", "x"}, // row0: col0, col1
		{"x", "x"}, // row1: col0, col1
	})
	v.SetSearch("x")
	_, cur, total, _ := v.SearchStatus()
	require.Equal(t, 4, total)
	require.Equal(t, 1, cur, "cursor at (0,0): current is the first match")

	// Reading order is (0,0),(0,1),(1,0),(1,1) — verify cursor walks it.
	v.NextMatch()
	r, c := v.CursorPosition()
	require.Equal(t, 0, r)
	require.Equal(t, 1, c, "second match is row 0 col 1")

	v.NextMatch()
	r, c = v.CursorPosition()
	require.Equal(t, 1, r)
	require.Equal(t, 0, c, "third match is row 1 col 0")

	v.NextMatch()
	r, c = v.CursorPosition()
	require.Equal(t, 1, r)
	require.Equal(t, 1, c, "fourth match is row 1 col 1")
}

// TestNextMatch_WrapsAround pins: NextMatch from the last match wraps to
// the first.
func TestNextMatch_WrapsAround(t *testing.T) {
	v := twoColView(t, [][]any{
		{"a", ""},
		{"a", ""},
	})
	v.SetSearch("a")
	_, _, total, _ := v.SearchStatus()
	require.Equal(t, 2, total)

	v.NextMatch() // -> match 2 (row1,col0)
	r, _ := v.CursorPosition()
	require.Equal(t, 1, r)

	v.NextMatch() // wrap -> match 1 (row0,col0)
	r, _ = v.CursorPosition()
	require.Equal(t, 0, r)

	_, cur, _, _ := v.SearchStatus()
	require.Equal(t, 1, cur, "wrapped back to first match")
}

// TestPrevMatch_WrapsAround pins: PrevMatch from the first match wraps to
// the last.
func TestPrevMatch_WrapsAround(t *testing.T) {
	v := twoColView(t, [][]any{
		{"a", ""},
		{"a", ""},
	})
	v.SetSearch("a") // current = match 1 (row0,col0)
	v.PrevMatch()    // wrap to last (row1,col0)
	r, _ := v.CursorPosition()
	require.Equal(t, 1, r)
	_, cur, total, _ := v.SearchStatus()
	require.Equal(t, total, cur, "PrevMatch from first wraps to last")
}

// TestSetSearch_EmptyClears pins: an empty query clears the search.
func TestSetSearch_EmptyClears(t *testing.T) {
	v := twoColView(t, [][]any{{"a", "b"}})
	v.SetSearch("a")
	require.True(t, v.SearchActive())
	v.SetSearch("")
	require.False(t, v.SearchActive())
	_, cur, total, active := v.SearchStatus()
	require.Equal(t, 0, cur)
	require.Equal(t, 0, total)
	require.False(t, active)
}

// TestClearSearch pins ClearSearch drops the state.
func TestClearSearch(t *testing.T) {
	v := twoColView(t, [][]any{{"a", "b"}})
	v.SetSearch("a")
	require.True(t, v.SearchActive())
	v.ClearSearch()
	require.False(t, v.SearchActive())
}

// TestSearchActive_ZeroMatchesStillActive pins: a query that matched
// nothing is still active (status shows 0/0).
func TestSearchActive_ZeroMatchesStillActive(t *testing.T) {
	v := twoColView(t, [][]any{{"a", "b"}})
	v.SetSearch("zzz")
	require.True(t, v.SearchActive(), "non-empty query with 0 matches is still active")
	_, cur, total, active := v.SearchStatus()
	require.Equal(t, 0, cur)
	require.Equal(t, 0, total)
	require.True(t, active)
}

// TestNextMatch_ZeroMatchesNoOp pins: NextMatch/PrevMatch with 0 matches
// are no-ops and don't move the cursor.
func TestNextMatch_ZeroMatchesNoOp(t *testing.T) {
	v := twoColView(t, [][]any{{"a", "b"}})
	v.SetSearch("zzz")
	v.NextMatch()
	v.PrevMatch()
	r, c := v.CursorPosition()
	require.Equal(t, 0, r)
	require.Equal(t, 0, c)
}

// TestNextMatch_SingleMatchStays pins: with one match NextMatch/PrevMatch
// keep the cursor on it.
func TestNextMatch_SingleMatchStays(t *testing.T) {
	v := twoColView(t, [][]any{
		{"a", ""},
		{"b", ""},
	})
	v.SetSearch("a")
	v.NextMatch()
	r, _ := v.CursorPosition()
	require.Equal(t, 0, r, "single match: NextMatch stays put")
	v.PrevMatch()
	r, _ = v.CursorPosition()
	require.Equal(t, 0, r, "single match: PrevMatch stays put")
}

// TestSetSearch_MultibyteOffsets pins: byte offsets are into the original
// (renderCellPlain) string, UTF-8 boundary safe, even when case folding a
// multibyte rune.
func TestSetSearch_MultibyteOffsets(t *testing.T) {
	// "héllo" — 'é' is 2 bytes (0xC3 0xA9). Search "llo".
	v := twoColView(t, [][]any{{"héllo", ""}})
	v.SetSearch("llo")
	v.mu.RLock()
	matches := v.searchState.matches
	v.mu.RUnlock()
	require.Len(t, matches, 1)
	cell := renderCellPlain("héllo", models.ColumnMeta{TypeName: "text"})
	m := matches[0]
	require.Equal(t, "llo", cell[m.byteStart:m.byteEnd], "offsets must slice the original string")
}

// TestSetSearch_MultibyteCaseFold pins: case-insensitive matching of a
// multibyte query still yields original-string offsets. Uppercase letters
// like 'İ' vs 'i' would change byte length, so we use a Latin-1 supplement
// case pair: 'É' (2 bytes) lowercases to 'é' (2 bytes); the query "é"
// (lowercase => case-insensitive) must match the "É" in the cell.
func TestSetSearch_MultibyteCaseFold(t *testing.T) {
	v := twoColView(t, [][]any{{"aÉb", ""}})
	v.SetSearch("é") // lowercase query => case-insensitive
	v.mu.RLock()
	matches := v.searchState.matches
	v.mu.RUnlock()
	require.Len(t, matches, 1, "case-insensitive match of 'é' against 'É'")
	cell := renderCellPlain("aÉb", models.ColumnMeta{TypeName: "text"})
	m := matches[0]
	require.Equal(t, "É", cell[m.byteStart:m.byteEnd], "offsets point at the original uppercase rune")
}

// TestSetSearch_ControlByteSanitizeAlignment pins the T2 offset contract:
// the matcher runs on the SANITIZED renderCellPlain output, so offsets
// align to the sanitized string (control bytes stripped), not the raw value.
func TestSetSearch_ControlByteSanitizeAlignment(t *testing.T) {
	// Embed a C0 control byte (0x01) that SanitizeCellEscapes strips. The
	// raw value is "ab\x01cd"; sanitized is "abcd". A search for "bc" must
	// span the sanitized string's bytes 1..3.
	raw := "ab\x01cd"
	v := twoColView(t, [][]any{{raw, ""}})
	v.SetSearch("bc")
	v.mu.RLock()
	matches := v.searchState.matches
	v.mu.RUnlock()
	require.Len(t, matches, 1)
	cell := renderCellPlain(raw, models.ColumnMeta{TypeName: "text"})
	require.Equal(t, "abcd", cell, "precondition: control byte stripped by SanitizeCellEscapes")
	m := matches[0]
	require.Equal(t, "bc", cell[m.byteStart:m.byteEnd], "offsets align to the sanitized string")
	require.Equal(t, 1, m.byteStart)
	require.Equal(t, 3, m.byteEnd)
}

// TestNextMatch_StaleIndexAfterShrinkNoPanic pins: if the buffer shrinks
// (SetColumns) after SetSearch, NextMatch must not panic. SetColumns clears
// search, so this is the safe path; the explicit bounds-check in
// moveCursorToCurrentMatchLocked guards the residual.
func TestNextMatch_StaleIndexAfterShrinkNoPanic(t *testing.T) {
	v := twoColView(t, [][]any{
		{"x", "x"},
		{"x", "x"},
	})
	v.SetSearch("x")
	// SetColumns resets the buffer (and clears search).
	v.SetColumns([]models.ColumnMeta{{Name: "only", TypeName: "text"}})
	require.NotPanics(t, func() {
		v.NextMatch()
		v.PrevMatch()
	}, "stale match list after shrink must not panic")
	require.False(t, v.SearchActive(), "SetColumns clears the search")
}

// TestSetSearch_NoDeadlockWithAppendRows mirrors
// TestSetFilter_NoDeadlockWithAppendRows: SetSearch must briefly take the
// write lock and never call snapshot()/Render under it, so concurrent
// AppendRows keeps running. The test completes (via -timeout) and all rows
// are present.
func TestSetSearch_NoDeadlockWithAppendRows(t *testing.T) {
	v := twoColView(t, nil)

	const goroutines = 8
	const perGoroutine = 50
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			batch := make([]models.Row, perGoroutine)
			for j := range batch {
				batch[j] = models.Row{Values: []any{"x", "y"}}
			}
			v.AppendRows(batch)
		}()
		go func(i int) {
			defer wg.Done()
			q := "x"
			if i%2 == 0 {
				q = "y"
			}
			v.SetSearch(q)
			v.NextMatch()
		}(i)
	}
	wg.Wait()

	require.Equal(t, goroutines*perGoroutine, v.RowCount())
}

// TestSetSearch_SnapshotIsDefensiveCopy pins: the snapshot's match list is
// a fresh allocation, not an alias of the live slice, so a held frame can't
// tear when SetSearch replaces the live slice.
func TestSetSearch_SnapshotIsDefensiveCopy(t *testing.T) {
	v := twoColView(t, [][]any{{"a", ""}})
	v.SetSearch("a")
	snap := v.snapshot()
	require.Len(t, snap.searchMatches, 1)
	// Replace the live slice via a fresh search; the snapshot must be
	// unaffected.
	v.SetSearch("zzz")
	require.Len(t, snap.searchMatches, 1, "snapshot match list must be a defensive copy")
	require.Equal(t, "a", snap.searchQuery)
}
