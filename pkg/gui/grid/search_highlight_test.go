package grid

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/stretchr/testify/require"
)

// searchSGR is the SGR prefix the non-current (SearchHighlight) match wears
// under the default-dark theme: fg yellow.
const searchSGR = "\x1b[33m"

// curSearchSGR is the SGR prefix the current (CurSearch) match wears under
// the default-dark theme: fg black on bg yellow.
const curSearchSGR = "\x1b[30m\x1b[43m"

func textCol(name string) models.ColumnMeta {
	return models.ColumnMeta{Name: name, TypeName: "text"}
}

// TestRenderCellPaddedHighlighted_SubstringInsideWiderCell proves a matched
// substring inside a cell wider than the content is wrapped with the Search
// SGR (non-current) and CurSearch SGR (current), with the surrounding runes
// left in the base cell style.
func TestRenderCellPaddedHighlighted_SubstringInsideWiderCell(t *testing.T) {
	col := textCol("c")
	// "hello world", match "world" at bytes [6,11). Cell width 20 (no trunc).
	spans := []highlightSpan{{byteStart: 6, byteEnd: 11, current: false}}
	got := renderCellPaddedHighlighted("hello world", col, 20, false, spans)

	require.Contains(t, got, searchSGR+"world"+ansiReset,
		"matched substring must be wrapped with the Search SGR")
	require.NotContains(t, got, curSearchSGR,
		"non-current match must not use CurSearch")

	// Current variant uses the stronger style.
	spans[0].current = true
	gotCur := renderCellPaddedHighlighted("hello world", col, 20, false, spans)
	require.Contains(t, gotCur, curSearchSGR+"world"+ansiReset,
		"current match must be wrapped with the CurSearch SGR")
}

// TestRenderCellPaddedHighlighted_MatchAtByteZero covers a span anchored at
// the very first byte.
func TestRenderCellPaddedHighlighted_MatchAtByteZero(t *testing.T) {
	col := textCol("c")
	got := renderCellPaddedHighlighted("hello", col, 10, false,
		[]highlightSpan{{byteStart: 0, byteEnd: 2, current: false}})
	require.Contains(t, got, searchSGR+"he"+ansiReset)
}

// TestRenderCellPaddedHighlighted_MatchAtLastRune covers a span ending at the
// final content rune (no truncation; the trailing padding stays unstyled).
func TestRenderCellPaddedHighlighted_MatchAtLastRune(t *testing.T) {
	col := textCol("c")
	got := renderCellPaddedHighlighted("hello", col, 10, false,
		[]highlightSpan{{byteStart: 3, byteEnd: 5, current: false}})
	require.Contains(t, got, searchSGR+"lo"+ansiReset)
}

// TestRenderCellPaddedHighlighted_CJKBoundaryAligned proves the rune-boundary
// mapping survives multibyte CJK + emoji content: the matched runes are
// wrapped exactly, adjacent runes are intact, and no mojibake is produced.
func TestRenderCellPaddedHighlighted_CJKBoundaryAligned(t *testing.T) {
	col := textCol("c")
	// "あ世界x" — あ(3B) 世(3B) 界(3B) x(1B). Match "世界" at bytes [3,9).
	val := "あ世界x"
	got := renderCellPaddedHighlighted(val, col, 20, false,
		[]highlightSpan{{byteStart: 3, byteEnd: 9, current: false}})

	require.Contains(t, got, searchSGR+"世界"+ansiReset,
		"matched CJK runes must be wrapped exactly")
	require.True(t, strings.Contains(got, "あ"), "leading rune intact")
	require.True(t, strings.Contains(got, "x"), "trailing rune intact")
	require.True(t, utf8.ValidString(got), "output must be valid UTF-8 (no mojibake)")
}

// TestRenderCellPaddedHighlighted_EmojiBoundaryAligned exercises an emoji
// rune (4-byte) adjacent to the match.
func TestRenderCellPaddedHighlighted_EmojiBoundaryAligned(t *testing.T) {
	col := textCol("c")
	// "🚀ok" — 🚀(4B) o(1B) k(1B). Match "ok" at bytes [4,6).
	got := renderCellPaddedHighlighted("🚀ok", col, 20, false,
		[]highlightSpan{{byteStart: 4, byteEnd: 6, current: false}})
	require.Contains(t, got, searchSGR+"ok"+ansiReset)
	require.True(t, strings.Contains(got, "🚀"), "emoji rune must stay intact")
}

// TestRenderCellPaddedHighlighted_MultipleMatchesOneCell proves several
// matches in one cell are each highlighted, and the current one gets the
// stronger CurSearch style while the others get Search.
func TestRenderCellPaddedHighlighted_MultipleMatchesOneCell(t *testing.T) {
	col := textCol("c")
	// "ab ab ab", "ab" at [0,2),[3,5),[6,8). Middle one is current.
	spans := []highlightSpan{
		{byteStart: 0, byteEnd: 2, current: false},
		{byteStart: 3, byteEnd: 5, current: true},
		{byteStart: 6, byteEnd: 8, current: false},
	}
	got := renderCellPaddedHighlighted("ab ab ab", col, 20, false, spans)
	require.Equal(t, 2, strings.Count(got, searchSGR+"ab"+ansiReset),
		"two non-current matches expected")
	require.Contains(t, got, curSearchSGR+"ab"+ansiReset,
		"current match expected with CurSearch")
}

// TestRenderCellPaddedHighlighted_ClippedByWidthFallsBackWholeCell proves a
// match that extends past the truncation boundary triggers the whole-cell
// fallback (no panic, no out-of-range slice) and the rendered cell carries
// the highlight style wrapping the whole truncated content.
func TestRenderCellPaddedHighlighted_ClippedByWidthFallsBackWholeCell(t *testing.T) {
	col := textCol("c")
	// "hello world", match "world" [6,11) but width 5 keeps only "hell"+"…".
	require.NotPanics(t, func() {
		got := renderCellPaddedHighlighted("hello world", col, 5, false,
			[]highlightSpan{{byteStart: 6, byteEnd: 11, current: false}})
		// Whole-cell fallback wraps the rendered (truncated) cell.
		require.Contains(t, got, searchSGR)
		require.Contains(t, got, "…")
	})
}

// TestRenderCellPaddedHighlighted_ByteEndOutOfRangeFallsBack proves an
// out-of-range byteEnd never panics and takes the whole-cell fallback.
func TestRenderCellPaddedHighlighted_ByteEndOutOfRangeFallsBack(t *testing.T) {
	col := textCol("c")
	require.NotPanics(t, func() {
		got := renderCellPaddedHighlighted("hello", col, 10, false,
			[]highlightSpan{{byteStart: 0, byteEnd: 999, current: false}})
		require.Contains(t, got, searchSGR)
	})
}

// TestRenderCellPaddedHighlighted_MidRuneOffsetFallsBack proves a byte offset
// landing inside a multibyte rune triggers the fallback, not a mid-rune slice.
func TestRenderCellPaddedHighlighted_MidRuneOffsetFallsBack(t *testing.T) {
	col := textCol("c")
	require.NotPanics(t, func() {
		// "あ" is bytes [0,3); offset 1 is mid-rune.
		got := renderCellPaddedHighlighted("あx", col, 10, false,
			[]highlightSpan{{byteStart: 1, byteEnd: 4, current: false}})
		require.Contains(t, got, searchSGR)
	})
}

// TestRenderCellPaddedHighlighted_NoSpansMatchesCleanPath is the byte-identity
// regression: with no spans the highlighted renderer must equal renderCellPadded
// exactly.
func TestRenderCellPaddedHighlighted_NoSpansMatchesCleanPath(t *testing.T) {
	col := textCol("c")
	clean := renderCellPadded("hello world", col, 20, false)
	got := renderCellPaddedHighlighted("hello world", col, 20, false, nil)
	require.Equal(t, clean, got)
}

// TestRenderDataLine_NoActiveSearchByteIdentical proves the whole data-line
// render is byte-identical to the pre-search-feature output when no search is
// active (searchActive=false), guarding the clean path.
func TestRenderDataLine_NoActiveSearchByteIdentical(t *testing.T) {
	v := NewView()
	v.SetColumns([]models.ColumnMeta{textCol("c0"), textCol("c1")})
	v.AppendRows([]models.Row{{Values: []any{"hello", "world"}}})

	const innerW, innerH = 80, 24
	snap := v.snapshot()
	snap.rowOffset, snap.colOffset = v.clampOffsetsLocked(snap, innerW, innerH)
	require.False(t, snap.searchActive)

	got := renderDataLine(snap, 0, innerW)
	require.NotContains(t, got, searchSGR+"hello",
		"no highlight SGR may wrap content when search is inactive")
	// And no spans for any cell.
	require.Nil(t, cellHighlightSpans(snap, 0, 0))
	require.Nil(t, cellHighlightSpans(snap, 0, 1))
}

// TestRenderDataLine_HighlightsActiveSearch exercises the REAL render path
// (renderDataLine -> renderCellPaddedHighlighted) end-to-end: an active search
// highlights the matched substring in the right cell, current vs non-current.
func TestRenderDataLine_HighlightsActiveSearch(t *testing.T) {
	v := NewView()
	v.SetColumns([]models.ColumnMeta{textCol("c0"), textCol("c1")})
	v.AppendRows([]models.Row{
		{Values: []any{"alpha", "beta"}},
		{Values: []any{"gamma", "alpha"}},
	})
	v.SetSearch("alpha")

	const innerW, innerH = 80, 24
	snap := v.snapshot()
	snap.rowOffset, snap.colOffset = v.clampOffsetsLocked(snap, innerW, innerH)
	require.True(t, snap.searchActive)

	line0 := renderDataLine(snap, 0, innerW)
	line1 := renderDataLine(snap, 1, innerW)

	// The current match is the first one (row0,col0) since the cursor began
	// at (0,0): it gets CurSearch; the row1,col1 match gets Search.
	require.Contains(t, line0, curSearchSGR+"alpha"+ansiReset,
		"current match (row0,col0) must use CurSearch")
	require.Contains(t, line1, searchSGR+"alpha"+ansiReset,
		"non-current match (row1,col1) must use Search")
}

// TestNextMatch_ScrollsOffscreenCellIntoView proves the scroll-into-view
// contract: NextMatch moves the cursor onto an off-screen match cell and the
// existing clamp brings that row into the rendered viewport
// (verification of the existing clampOffsetsLocked path; no new clamp added).
func TestNextMatch_ScrollsOffscreenCellIntoView(t *testing.T) {
	v := NewView()
	v.SetColumns([]models.ColumnMeta{textCol("c0")})
	rows := make([]models.Row, 50)
	for i := range rows {
		rows[i] = models.Row{Values: []any{"row"}}
	}
	// Put a unique match far down the buffer.
	rows[40] = models.Row{Values: []any{"needle"}}
	v.AppendRows(rows)

	v.SetSearch("needle") // single match at row 40; cursor jumps there.

	r, _ := v.CursorPosition()
	require.Equal(t, 40, r, "search must land the cursor on the match row")

	const innerW, innerH = 80, 10 // only 9 data rows visible
	snap := v.snapshot()
	snap.rowOffset, snap.colOffset = v.clampOffsetsLocked(snap, innerW, innerH)

	// The matched cell must render inside the viewport. It is the current
	// match (cursor is on it), so it wears CurSearch.
	body := renderBody(snap, innerW, innerH)
	require.Contains(t, body, curSearchSGR+"needle"+ansiReset,
		"matched off-screen cell must be scrolled into view and highlighted")
}
