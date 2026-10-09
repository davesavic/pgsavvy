package grid

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/davesavic/pgsavvy/pkg/models"
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

// TestSearchJumpFiresCursorChange pins the relationship-panel live-follow
// gap: the cursor-change callback must fire on search-jump (initial
// SetSearch jump) and on n/N navigation (NextMatch/PrevMatch), exactly as
// it does for j/k/G. Without the fire in moveCursorToCurrentMatchLocked the
// panel silently fails to follow search-driven cursor motion.
func TestSearchJumpFiresCursorChange(t *testing.T) {
	v := twoColView(t, [][]any{
		{"x", "x"},
		{"x", "x"},
	})
	var fired int
	var lastRow, lastCol int
	v.SetOnCursorChange(func(row, col int) {
		fired++
		lastRow, lastCol = row, col
	})

	// Initial search-jump lands the cursor on the first match (0,0) and
	// must fire the callback.
	v.SetSearch("x")
	require.Equal(t, 1, fired, "SetSearch jump must fire the cursor-change callback")
	require.Equal(t, 0, lastRow)
	require.Equal(t, 0, lastCol)

	// n -> second match (0,1): another fire with the new position.
	v.NextMatch()
	require.Equal(t, 2, fired, "NextMatch (n) must fire the cursor-change callback")
	require.Equal(t, 0, lastRow)
	require.Equal(t, 1, lastCol)

	// N -> back to first match (0,0): another fire.
	v.PrevMatch()
	require.Equal(t, 3, fired, "PrevMatch (N) must fire the cursor-change callback")
	require.Equal(t, 0, lastRow)
	require.Equal(t, 0, lastCol)
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

	for i := range goroutines {
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
	_, _, total, active := v.SearchStatus()
	require.True(t, active)
	require.Equal(t, goroutines*perGoroutine, total, "every row matches either concurrent query")
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

func TestSearchIncludesAppendedRows(t *testing.T) {
	v := twoColView(t, [][]any{{"before", ""}})
	v.SetSearch("hit")
	empty := v.snapshot()
	v.AppendRows([]models.Row{
		{Values: []any{"hit hit", "HiT"}},
		{Values: []any{"éhit", "x"}},
	})
	query, cur, total, active := v.SearchStatus()
	require.Equal(t, "hit", query)
	require.True(t, active)
	require.Equal(t, 1, cur)
	require.Equal(t, 4, total)
	want := []cellMatch{
		{row: 1, col: 0, byteStart: 0, byteEnd: 3},
		{row: 1, col: 0, byteStart: 4, byteEnd: 7},
		{row: 1, col: 1, byteStart: 0, byteEnd: 3},
		{row: 2, col: 0, byteStart: 2, byteEnd: 5},
	}
	snap := v.snapshot()
	require.Equal(t, want, snap.searchMatches)
	require.Empty(t, empty.searchMatches, "an earlier frame must not gain new matches")
	line := renderDataLine(snap, 1, 80)
	require.Contains(t, line, curSearchSGR+"hit"+ansiReset)
	require.Contains(t, line, searchSGR+"hit"+ansiReset)
	require.Contains(t, line, searchSGR+"HiT"+ansiReset)
	for _, step := range []struct {
		previous bool
		row, col int
		current  int
	}{
		{previous: true, row: 2, col: 0, current: 4},
		{row: 1, col: 0, current: 1},
		{row: 1, col: 0, current: 2},
		{row: 1, col: 1, current: 3},
	} {
		if step.previous {
			v.PrevMatch()
		} else {
			v.NextMatch()
		}
		row, col := v.CursorPosition()
		require.Equal(t, [2]int{step.row, step.col}, [2]int{row, col})
		_, current, _, _ := v.SearchStatus()
		require.Equal(t, step.current, current)
	}
	v.AppendRows(nil)
	v.AppendRows([]models.Row{{Values: []any{"more hit", "hit"}}})
	want = append(want, cellMatch{row: 3, col: 0, byteStart: 5, byteEnd: 8}, cellMatch{row: 3, col: 1, byteStart: 0, byteEnd: 3})
	require.Equal(t, want, v.snapshot().searchMatches)
	require.Equal(t, want[:4], snap.searchMatches, "held frame must retain its original match list")
	_, current, total, _ := v.SearchStatus()
	require.Equal(t, 3, current)
	require.Equal(t, 6, total)
}

func TestSearchAppendPreservesCurrentAndResets(t *testing.T) {
	v := twoColView(t, [][]any{{"hit hit", "hit"}})
	v.SetSearch("hit")
	v.NextMatch()
	before := v.snapshot()
	v.AppendRows([]models.Row{{Values: []any{"hit", "hit"}}})
	after := v.snapshot()
	require.Equal(t, before.searchCurrentIdx, after.searchCurrentIdx)
	require.Equal(t, before.searchMatches[before.searchCurrentIdx], after.searchMatches[after.searchCurrentIdx])
	row, col := v.CursorPosition()
	require.Equal(t, [2]int{0, 0}, [2]int{row, col}, "streaming must not move the cursor")
	v.SetSearch("Hit")
	v.AppendRows([]models.Row{{Values: []any{"hit", "Hit"}}})
	require.Equal(t, []cellMatch{{row: 2, col: 1, byteStart: 0, byteEnd: 3}}, v.snapshot().searchMatches, "new query and smart case replace the old match set")
	v.ClearSearch()
	v.AppendRows([]models.Row{{Values: []any{"Hit", "hit"}}})
	require.False(t, v.SearchActive())
	require.Empty(t, v.snapshot().searchMatches)
	v.SetSearch("hit")
	v.SetColumns([]models.ColumnMeta{{Name: "filtered", TypeName: "text"}})
	v.AppendRows([]models.Row{{Values: []any{"hit"}}})
	require.False(t, v.SearchActive(), "new result schema must not retain an earlier search")
	require.Empty(t, v.snapshot().searchMatches)
	v.SetSearch("hit")
	require.Equal(t, []cellMatch{{row: 0, col: 0, byteStart: 0, byteEnd: 3}}, v.snapshot().searchMatches)
}
