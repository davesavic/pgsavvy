package grid

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/theme"
	"github.com/davesavic/pgsavvy/pkg/theme/builtin"
)

// TestCursorColumnVisibleAfterHorizontalScroll proves the horizontal
// clamp keeps the cursor column on-screen. The clamp (clampOffsetsLocked)
// and the renderer (renderDataLine) must agree on how much width each
// column consumes; if the clamp under-counts the inter-column separator
// it leaves colOffset too small and the renderer truncates the cursor
// cell off the right edge. pgsavvy column-scroll fix.
func TestCursorColumnVisibleAfterHorizontalScroll(t *testing.T) {
	v := NewView()
	cols := make([]models.ColumnMeta, 0, 6)
	vals := make([]any, 0, 6)
	for i := range 6 {
		cols = append(cols, models.ColumnMeta{Name: fmt.Sprintf("c%d", i), TypeName: "text"})
		// "valN_" + 9 'x' == 14 visible columns each.
		vals = append(vals, fmt.Sprintf("val%d_%s", i, strings.Repeat("x", 9)))
	}
	v.SetColumns(cols)
	v.AppendRows([]models.Row{{Values: vals}})
	// Pin uniform widths so the arithmetic is deterministic regardless of
	// auto-size sampling.
	v.widths = []int{14, 14, 14, 14, 14, 14}

	// Cursor on the last column; the viewport must scroll right to show it.
	v.JumpColLast()

	const innerW, innerH = 80, 24
	snap := v.snapshot()
	snap.rowOffset, snap.colOffset = v.clampOffsetsLocked(snap, innerW, innerH)

	line := renderDataLine(snap, 0, innerW)
	require.Contains(t, line, "val5",
		"cursor column (col 5) must be visible after scroll; clamp chose colOffset=%d but renderer truncated the cursor cell",
		snap.colOffset)
}

// TestColumnScrollHints verifies the ‹ / › header arrows appear exactly
// when non-hidden columns sit beyond the viewport edges. pgsavvy
// column-scroll indicator.
func TestColumnScrollHints(t *testing.T) {
	v := NewView()
	cols := make([]models.ColumnMeta, 0, 6)
	vals := make([]any, 0, 6)
	for i := range 6 {
		cols = append(cols, models.ColumnMeta{Name: fmt.Sprintf("c%d", i), TypeName: "text"})
		vals = append(vals, fmt.Sprintf("val%d_%s", i, strings.Repeat("x", 9)))
	}
	v.SetColumns(cols)
	v.AppendRows([]models.Row{{Values: vals}})
	v.widths = []int{14, 14, 14, 14, 14, 14}

	const innerW = 80

	// At col 0: columns overflow to the right, none hidden on the left.
	snap := v.snapshot()
	left, right := columnScrollHints(snap, innerW)
	require.False(t, left, "no left arrow at the start")
	require.True(t, right, "right arrow when columns overflow")
	require.Contains(t, renderHeaderLine(snap, innerW, left, right), "►",
		"header must carry the ► arrow")
	require.NotContains(t, renderHeaderLine(snap, innerW, left, right), "◄",
		"no ◄ arrow at the start")

	// Scrolled fully right: cursor on last column, nothing hidden right.
	v.JumpColLast()
	snap = v.snapshot()
	snap.rowOffset, snap.colOffset = v.clampOffsetsLocked(snap, innerW, 24)
	left, right = columnScrollHints(snap, innerW)
	require.True(t, left, "left arrow once scrolled right")
	require.False(t, right, "no right arrow when the last column is visible")
	require.Contains(t, renderHeaderLine(snap, innerW, left, right), "◄",
		"header must carry the ◄ arrow when scrolled right")
}

// TestMoveCursorClampsAtEdges verifies the cursor verbs do not move past
// the row 0 / col 0 floors or past the last loaded row/col ceilings.
func TestMoveCursorClampsAtEdges(t *testing.T) {
	v := NewView()
	v.SetColumns([]models.ColumnMeta{
		{Name: "c0", TypeName: "text"},
		{Name: "c1", TypeName: "text"},
	})
	v.AppendRows([]models.Row{
		{Values: []any{"a", "b"}},
		{Values: []any{"c", "d"}},
	})

	// MoveCursorUp from row 0 stays at 0.
	v.MoveCursorUp()
	r, c := v.CursorPosition()
	require.Equal(t, 0, r)
	require.Equal(t, 0, c)

	// MoveCursorLeft from col 0 stays at col 0.
	v.MoveCursorLeft()
	r, c = v.CursorPosition()
	require.Equal(t, 0, r)
	require.Equal(t, 0, c)

	// MoveCursorDown past last row stays at last row.
	for range 10 {
		v.MoveCursorDown()
	}
	r, _ = v.CursorPosition()
	require.Equal(t, 1, r, "cursor row must clamp to len(rows)-1")

	// MoveCursorRight past last col stays at last col.
	for range 10 {
		v.MoveCursorRight()
	}
	_, c = v.CursorPosition()
	require.Equal(t, 1, c, "cursor col must clamp to len(cols)-1")
}

// TestJumpColFirstLast moves the cursor to the first / last column in
// grid mode (the 0 / $ chords).
func TestJumpColFirstLast(t *testing.T) {
	v := NewView()
	v.SetColumns([]models.ColumnMeta{
		{Name: "c0", TypeName: "text"},
		{Name: "c1", TypeName: "text"},
		{Name: "c2", TypeName: "text"},
	})
	v.AppendRows([]models.Row{{Values: []any{"a", "b", "c"}}})

	// $ from col 0 lands on the last column.
	v.JumpColLast()
	_, c := v.CursorPosition()
	require.Equal(t, 2, c, "JumpColLast must land on len(cols)-1")

	// 0 from the last column lands back on col 0.
	v.JumpColFirst()
	_, c = v.CursorPosition()
	require.Equal(t, 0, c, "JumpColFirst must land on col 0")
}

// fourColView is a 4-column, 1-row grid used by the hidden-column
// navigation tests below.
func fourColView() *View {
	v := NewView()
	v.SetColumns([]models.ColumnMeta{
		{Name: "c0", TypeName: "text"},
		{Name: "c1", TypeName: "text"},
		{Name: "c2", TypeName: "text"},
		{Name: "c3", TypeName: "text"},
	})
	v.AppendRows([]models.Row{{Values: []any{"a", "b", "c", "d"}}})
	return v
}

// TestMoveCursorRight_SkipsHiddenColumns verifies right-motion steps over
// a run of hidden columns instead of parking the cursor on one (where it
// would render invisibly). pgsavvy hidden-col navigation fix.
func TestMoveCursorRight_SkipsHiddenColumns(t *testing.T) {
	v := fourColView()
	v.SetHiddenCols(map[int]bool{1: true, 2: true})

	v.MoveCursorRight()
	_, c := v.CursorPosition()
	require.Equal(t, 3, c, "right from col 0 must skip hidden 1,2 to land on 3")
}

// TestMoveCursorLeft_SkipsHiddenColumns is the symmetric counterpart.
func TestMoveCursorLeft_SkipsHiddenColumns(t *testing.T) {
	v := fourColView()
	v.SetHiddenCols(map[int]bool{1: true, 2: true})
	v.JumpColLast()

	v.MoveCursorLeft()
	_, c := v.CursorPosition()
	require.Equal(t, 0, c, "left from col 3 must skip hidden 2,1 to land on 0")
}

// TestMoveCursorRight_NoVisibleColumnToRightStays verifies the cursor
// holds position when every column to the right is hidden.
func TestMoveCursorRight_NoVisibleColumnToRightStays(t *testing.T) {
	v := fourColView()
	v.SetHiddenCols(map[int]bool{2: true, 3: true})
	v.MoveCursorRight() // 0 -> 1

	v.MoveCursorRight() // 1 -> nothing visible to the right
	_, c := v.CursorPosition()
	require.Equal(t, 1, c, "right must stay put when only hidden columns remain")
}

// TestJumpColLast_LandsOnLastVisibleColumn verifies $ lands on the last
// visible column, not a hidden trailing column.
func TestJumpColLast_LandsOnLastVisibleColumn(t *testing.T) {
	v := fourColView()
	v.SetHiddenCols(map[int]bool{3: true})

	v.JumpColLast()
	_, c := v.CursorPosition()
	require.Equal(t, 2, c, "$ must land on last visible column (2), not hidden 3")
}

// TestJumpColFirst_LandsOnFirstVisibleColumn verifies 0/^ lands on the
// first visible column, not a hidden leading column.
func TestJumpColFirst_LandsOnFirstVisibleColumn(t *testing.T) {
	v := fourColView()
	v.SetHiddenCols(map[int]bool{0: true})
	v.JumpColLast()

	v.JumpColFirst()
	_, c := v.CursorPosition()
	require.Equal(t, 1, c, "0 must land on first visible column (1), not hidden 0")
}

// TestSetHiddenCols_SnapsCursorOffNewlyHiddenColumn verifies that hiding
// the column the cursor sits on moves the cursor to a visible neighbor so
// it never renders invisibly. pgsavvy hidden-col navigation fix.
func TestSetHiddenCols_SnapsCursorOffNewlyHiddenColumn(t *testing.T) {
	v := fourColView()
	v.SetCursor(0, 2)

	v.SetHiddenCols(map[int]bool{2: true})
	_, c := v.CursorPosition()
	require.Equal(t, 3, c, "hiding col 2 under the cursor must snap to visible 3")
}

// TestSetCursor_SnapsOffHiddenColumn verifies a programmatic SetCursor
// onto a hidden column lands on the nearest visible column instead.
func TestSetCursor_SnapsOffHiddenColumn(t *testing.T) {
	v := fourColView()
	v.SetHiddenCols(map[int]bool{1: true})

	v.SetCursor(0, 1)
	_, c := v.CursorPosition()
	require.Equal(t, 2, c, "SetCursor onto hidden col 1 must snap to visible 2")
}

// TestHalfPageDown_Step verifies HalfPageDown advances by ResultPageSize/2
// when there's enough room, and clamps to the last row otherwise.
func TestHalfPageDown_Step(t *testing.T) {
	v := NewView()
	v.SetColumns(makeSingleCol("c1", "text"))
	rows := make([]models.Row, ResultPageSize)
	for i := range rows {
		rows[i] = models.Row{Values: []any{"x"}}
	}
	v.AppendRows(rows)

	startRow, _ := v.CursorPosition()
	v.HalfPageDown()
	endRow, _ := v.CursorPosition()
	step := endRow - startRow
	expected := ResultPageSize / 2
	require.Equal(t, expected, step,
		"HalfPageDown should move by ResultPageSize/2 when not clamped")
}

// TestRender_VerticalScroll_FollowsCursor appends 100 rows, drives the
// cursor to row 50, then renders into a 10-row tall target. The rendered
// output should not contain row 0's content but should contain the
// cursor row's content.
func TestRender_VerticalScroll_FollowsCursor(t *testing.T) {
	v := NewView()
	v.SetColumns(makeSingleCol("c1", "text"))
	rows := make([]models.Row, 100)
	for i := range rows {
		// Use unique distinguishable values.
		rows[i] = models.Row{Values: []any{rowLabel(i)}}
	}
	v.AppendRows(rows)

	for range 50 {
		v.MoveCursorDown()
	}

	// 10-row target: y0=0, y1=11 → height 12, InnerHeight 10.
	target := newTallTestView("scroller", 10)
	v.Render(target)

	buf := target.Buffer()
	require.NotContains(t, buf, rowLabel(0),
		"row 0 must have scrolled out of view after cursor moved to row 50")
	require.Contains(t, buf, rowLabel(50),
		"the cursor row's content must be on-screen after Render")
}

// TestVisibleColumnOrder_FrozenFirstCol asserts that with the
// frozen-first-column toggle on, column 0's header is still rendered
// after horizontal scrolling.
func TestVisibleColumnOrder_FrozenFirstCol(t *testing.T) {
	v := NewView()
	v.SetColumns([]models.ColumnMeta{
		{Name: "frozen_col", TypeName: "text"},
		{Name: "scroll_col_1", TypeName: "text"},
		{Name: "scroll_col_2", TypeName: "text"},
		{Name: "scroll_col_3", TypeName: "text"},
		{Name: "scroll_col_4", TypeName: "text"},
	})
	v.AppendRows([]models.Row{
		{Values: []any{"v0", "v1", "v2", "v3", "v4"}},
	})
	v.ToggleFrozenFirstColumn()
	require.True(t, v.FrozenFirstColumn())

	// Scroll right several columns.
	for range 4 {
		v.MoveCursorRight()
	}

	target := newTallTestView("frozen", 5)
	v.Render(target)
	buf := target.Buffer()
	// The first non-empty line is the header. It must lead with the
	// frozen column's name even though the cursor is several columns
	// over.
	firstLine := firstNonEmptyLine(buf)
	require.True(t, strings.HasPrefix(strings.TrimLeft(firstLine, " "), "frozen_col"),
		"frozen first column's header must lead the rendered header, got %q",
		firstLine)
}

// TestMaybeFireNearTail_FiresOncePerCrossing verifies the prefetch
// callback fires exactly once per crossing into the near-tail window,
// then re-fires after AppendRows extends the buffer past the previous
// fire-point.
func TestMaybeFireNearTail_FiresOncePerCrossing(t *testing.T) {
	v := NewView()
	v.SetColumns(makeSingleCol("c1", "text"))

	var fires atomic.Int64
	v.SetOnNearTail(func(n int) {
		fires.Add(1)
	})

	// Append 30 rows. With PrefetchThreshold=25, the near-tail zone is
	// rows [5, 29]. Cursor starts at 0 (outside zone). Move into zone.
	rows := make([]models.Row, 30)
	for i := range rows {
		rows[i] = models.Row{Values: []any{"r"}}
	}
	v.AppendRows(rows)

	target := newTallTestView("nt", 5)
	// Step cursor into the near-tail zone and beyond, calling Render
	// after each step so maybeFireNearTail is checked.
	for range 28 {
		v.MoveCursorDown()
		v.Render(target)
	}
	require.Equal(t, int64(1), fires.Load(),
		"onNearTail should fire exactly once per crossing while rowsLen is unchanged")

	// Now extend the buffer past lastNearTailFireAt; the next render
	// inside the near-tail zone should re-fire.
	moreRows := make([]models.Row, 10)
	for i := range moreRows {
		moreRows[i] = models.Row{Values: []any{"r"}}
	}
	v.AppendRows(moreRows) // now 40 rows
	// JumpLast lands cursor at row 39, deep in the near-tail zone.
	v.JumpLast()
	v.Render(target)
	require.Equal(t, int64(2), fires.Load(),
		"onNearTail should re-fire after the buffer grows past the last fire point")
}

// TestJumpLast_LandsAtLastLoadedRow pins the grid-side cursor jump that
// ]p (Page(+1)) relies on.
func TestJumpLast_LandsAtLastLoadedRow(t *testing.T) {
	v := NewView()
	v.SetColumns(makeSingleCol("c1", "text"))
	rows := make([]models.Row, 17)
	for i := range rows {
		rows[i] = models.Row{Values: []any{rowLabel(i)}}
	}
	v.AppendRows(rows)
	v.JumpLast()
	r, _ := v.CursorPosition()
	require.Equal(t, 16, r, "JumpLast must land at len(rows)-1")
}

// TestHalfPageUp_MovesCursorUp pins the grid-side cursor rewind that
// [p (Page(-1)) relies on. The helper invokes HalfPageUp twice; the
// resulting net movement must be strictly upward when there's room.
func TestHalfPageUp_MovesCursorUp(t *testing.T) {
	v := NewView()
	v.SetColumns(makeSingleCol("c1", "text"))
	rows := make([]models.Row, ResultPageSize*2)
	for i := range rows {
		rows[i] = models.Row{Values: []any{"r"}}
	}
	v.AppendRows(rows)
	// Park cursor at tail.
	v.JumpLast()
	start, _ := v.CursorPosition()
	v.HalfPageUp()
	v.HalfPageUp()
	end, _ := v.CursorPosition()
	require.Less(t, end, start, "HalfPageUp x2 must move cursor up from %d, got %d", start, end)
}

// rowLabel returns a uniquely-identifiable string for a row index that's
// long enough not to collide with any of the constants used elsewhere
// in the tests. e.g. row 0 → "ROW-0000".
func rowLabel(i int) string {
	const base = "ROW-"
	var sb strings.Builder
	sb.WriteString(base)
	// Always pad to 4 digits; we never index past 999 in these tests.
	digits := []byte("0000")
	for d := 3; d >= 0; d-- {
		digits[d] = byte('0' + i%10)
		i /= 10
	}
	sb.Write(digits)
	return sb.String()
}

// newTallTestView builds a gocui.View with a configurable InnerHeight.
// Width is fixed to InnerWidth=80. InnerHeight = (y1 - y0 + 1) - 2 = rows.
func newTallTestView(name string, rows int) *gocui.View {
	// y0=0 → y1 = rows + 1 makes the total height rows+2 and the inner
	// height equal to rows.
	return gocui.NewView(name, 0, 0, 81, rows+1, gocui.OutputNormal)
}

// firstNonEmptyLine returns the first line in s that contains a
// non-whitespace character. Used to find the rendered header without
// depending on the exact number of blank padding lines.
func firstNonEmptyLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return ""
}

// TestRenderDataLine_DigitInSGRPrefixDoesNotCorruptEscape is a regression
// test for a bug where a numeric cell value (e.g. "3" or "5") collided
// with a digit inside the SGR prefix used to colourise it (e.g. \x1b[35m
// for magenta). The previous implementation used
// strings.Replace(decorated, visible, padded, 1) to splice the padded
// value into the already-decorated string; that replace matched the
// digit inside the SGR params rather than the cell value itself,
// corrupting the escape and leaking "[35m3" remnants onto the screen.
//
// The fix pads the plain visible string first and wraps it with the
// style afterwards. This test feeds an integer column whose values
// happen to share digits with the magenta SGR (3 and 5) and asserts the
// resulting line strips cleanly to plain text.
func TestRenderDataLine_DigitInSGRPrefixDoesNotCorruptEscape(t *testing.T) {
	v := NewView()
	v.SetColumns([]models.ColumnMeta{{Name: "id", TypeName: "int4"}})
	v.AppendRows([]models.Row{
		{Values: []any{1}},
		{Values: []any{3}}, // collides with '3' in \x1b[35m
		{Values: []any{5}}, // collides with '5' in \x1b[35m
	})
	snap := v.snapshot()
	for i := range 3 {
		line := renderDataLine(snap, i, 80)
		// Bug signature: a malformed CSI like "\x1b[3       5m" where
		// padding whitespace ended up *inside* the escape sequence
		// because strings.Replace matched a digit in the SGR params
		// instead of the cell value. Walk every \x1b[...] sequence
		// and assert no whitespace appears before its final byte.
		for j := 0; j < len(line); j++ {
			if line[j] != 0x1b || j+1 >= len(line) || line[j+1] != '[' {
				continue
			}
			for k := j + 2; k < len(line); k++ {
				b := line[k]
				if b == ' ' || b == '\t' {
					t.Fatalf("row %d: malformed CSI containing whitespace starting at offset %d (raw %q)",
						i, j, line)
				}
				if b >= 0x40 && b <= 0x7e {
					break // final byte reached
				}
			}
		}
	}
}

// TestRenderDataLine_CurrentMatchNotInvertedBySelection proves the n/N
// current match keeps its explicit CurSearch colors instead of being
// inverted by the reverse-video selection highlight. SetSearch/NextMatch
// always move the cursor onto the current match, so that cell is "in
// selection"; wrapping it in reverse-video swaps the black-on-yellow into
// yellow-on-black — the "black background" the highlight is supposed to
// avoid. The fix skips the reverse-video wrap on cells that
// carry search-match spans so the explicit highlight survives.
func TestRenderDataLine_CurrentMatchNotInvertedBySelection(t *testing.T) {
	v := NewView()
	v.SetColumns([]models.ColumnMeta{{Name: "name", TypeName: "text"}})
	v.AppendRows([]models.Row{{Values: []any{"alpha"}}})
	v.SetSearch("alpha") // current match; cursor moves onto its cell

	snap := v.snapshot()
	line := renderDataLine(snap, 0, 80)

	// The current match must carry the explicit CurSearch SGR (black on
	// yellow) — the intended high-contrast highlight.
	if !strings.Contains(line, curSearchSGR) {
		t.Fatalf("current match missing CurSearch SGR %q; line=%q", curSearchSGR, line)
	}
	// It must NOT be wrapped in reverse-video, which would swap the colors
	// into yellow-on-black (the dark-blob bug).
	if strings.Contains(line, ansiReverseVid) {
		t.Fatalf("current match cell wrapped in reverse-video inverts the highlight; line=%q", line)
	}
}

// TestRenderDataLine_DirtyCellShowsStagedValue verifies that once a result
// grid is wired to a PendingEditSet (SetPendingEdits) and has a row
// identity, renderDataLine renders a staged edit's NewValue — not the
// stale DB value — with the DirtyCell background tint. This is the
// integration the A3 feature was missing: the staged set must
// reach the render snapshot and be looked up per cell by PK + column.
func TestRenderDataLine_DirtyCellShowsStagedValue(t *testing.T) {
	v := NewView()
	v.SetColumns(catalogFixtureColumns([]models.ColumnMeta{
		{Name: "id", TypeName: "int4"},
		{Name: "name", TypeName: "text"},
	}))
	v.AppendRows([]models.Row{
		{Values: []any{1, "alice"}},
		{Values: []any{2, "carol"}},
	})
	// Row identity = column 0 (id); editable result.
	v.SetEditability(true, []int{0}, "", models.Ref{Schema: "public", Table: "t"})

	set := &models.PendingEditSet{}
	require.NoError(t, set.BindIdentity(v.EditIdentity()))
	if err := set.Add(models.PendingEdit{
		PrimaryKey: []any{1}, Column: "name",
		OldValue: "alice", NewValue: "bob", Kind: models.Literal,
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	v.SetPendingEdits(set)

	snap := v.snapshot()

	// Row 0 (id=1): name is staged alice -> bob.
	line0 := renderDataLine(snap, 0, 80)
	if !strings.Contains(line0, "bob") {
		t.Errorf("dirty cell must render staged NewValue 'bob'; line=%q", line0)
	}
	tint := bgEscape(theme.Current().DirtyCell.Bg)
	if tint == "" {
		t.Fatalf("default theme DirtyCell.Bg=%q produced no SGR", theme.Current().DirtyCell.Bg)
	}
	if !strings.Contains(line0, tint) {
		t.Errorf("dirty cell must carry the DirtyCell tint %q; line=%q", tint, line0)
	}
	if strings.Contains(line0, "alice") {
		t.Errorf("dirty cell must NOT render the stale value 'alice'; line=%q", line0)
	}

	// Row 1 (id=2): no staged edit — original value, no tint.
	line1 := renderDataLine(snap, 1, 80)
	if !strings.Contains(line1, "carol") {
		t.Errorf("clean row must render original value 'carol'; line=%q", line1)
	}
	if strings.Contains(line1, tint) {
		t.Errorf("clean row must not carry the dirty tint; line=%q", line1)
	}
}

func TestReview_json_scalar_dirty_raw_input(t *testing.T) {
	for _, raw := range []string{`{"a":2}`, `[1]`, `"true"`, `true`, `null`} {
		t.Run(raw, func(t *testing.T) {
			v := NewView()
			v.SetColumns(catalogFixtureColumns([]models.ColumnMeta{{Name: "id", TypeName: "int4"}, {Name: "payload", TypeName: "jsonb"}}))
			v.AppendRows([]models.Row{{Values: []any{1, "true"}}, {Values: []any{2, "true"}}})
			v.SetEditability(true, []int{0}, "", models.Ref{Schema: "public", Table: "t"})
			set := &models.PendingEditSet{}
			require.NoError(t, set.BindIdentity(v.EditIdentity()))
			require.NoError(t, set.Add(models.PendingEdit{PrimaryKey: []any{1}, Column: "payload", ColumnType: "jsonb", OldValue: "true", NewValue: raw, Kind: models.Literal}))
			v.SetPendingEdits(set)
			snap := v.snapshot()
			snap.widths[1] = 30
			line := stripANSI(renderDataLine(snap, 0, 80))
			require.Contains(t, line, raw)
			require.NotContains(t, line, `\"`)
			if raw == `true` || raw == `null` {
				require.NotContains(t, line, `"`+raw+`"`)
			}
			require.Contains(t, stripANSI(renderDataLine(snap, 1, 80)), `"true"`)
			require.Equal(t, raw, set.Edits()[0].NewValue)
		})
	}
}

// projectedView builds a single text-column view with the given label
// values in raw (insertion) order. Used by the projected-cursor navigation
// regression tests, which drive a non-contiguous projection via an active
// FILTER (the grid no longer reorders for sort).
func projectedView(t *testing.T, labels ...string) *View {
	t.Helper()
	v := NewView()
	v.SetColumns([]models.ColumnMeta{{Name: "name", TypeName: "text"}})
	for _, l := range labels {
		v.AppendRows([]models.Row{{Values: []any{l}}})
	}
	return v
}

// TestCursorNavigation_FollowsProjectedOrder is the regression
// test, updated so applyFilter is now identity, so the
// projection is always the full raw buffer in order and the cursor walks
// every row.
func TestCursorNavigation_FollowsProjectedOrder(t *testing.T) {
	v := projectedView(t, "match-0", "skip-1", "match-2", "skip-3", "match-4")
	require.Equal(t, []int{0, 1, 2, 3, 4}, projectIndices(v), "precondition: identity projection")

	// JumpFirst lands on raw 0.
	v.JumpFirst()
	r, _ := v.CursorPosition()
	require.Equal(t, 0, r, "JumpFirst must land on the first row")

	// Down walks every row: raw 0 -> 1 -> ... -> 4, then clamps.
	v.MoveCursorDown()
	r, _ = v.CursorPosition()
	require.Equal(t, 1, r)
	v.MoveCursorDown()
	r, _ = v.CursorPosition()
	require.Equal(t, 2, r)

	// JumpLast lands on the last row (raw 4).
	v.JumpLast()
	r, _ = v.CursorPosition()
	require.Equal(t, 4, r, "JumpLast must land on the last row")

	v.MoveCursorDown() // clamp at tail
	r, _ = v.CursorPosition()
	require.Equal(t, 4, r, "MoveCursorDown must clamp at the last row")

	// Up walks backwards; clamps at head.
	v.MoveCursorUp()
	r, _ = v.CursorPosition()
	require.Equal(t, 3, r)
	v.JumpFirst()
	v.MoveCursorUp() // clamp at head
	r, _ = v.CursorPosition()
	require.Equal(t, 0, r, "MoveCursorUp must clamp at the first row")
}

// TestRender_VerticalScroll_FollowsCursor is the viewport half,
// updated so that with applyFilter now identity the
// projection is the full buffer, so JumpLast lands on the raw tail and the
// viewport must scroll so that row is on-screen.
func TestRender_VerticalScroll_FollowsCursorToTail(t *testing.T) {
	v := NewView()
	v.SetColumns(makeSingleCol("c1", "text"))
	rows := make([]models.Row, 100)
	for i := range rows {
		rows[i] = models.Row{Values: []any{"row-" + rowLabel(i)}}
	}
	v.AppendRows(rows)

	v.JumpLast() // identity projection: last row == raw 99
	r, _ := v.CursorPosition()
	require.Equal(t, 99, r, "JumpLast lands on the raw tail under identity projection")

	target := newTallTestView("scrollfollow", 10)
	v.Render(target)
	buf := target.Buffer()

	require.Contains(t, buf, "row-"+rowLabel(r),
		"cursor row must be on-screen after JumpLast")
}

func TestReview_expanded_motion(t *testing.T) {
	v := makeView(makeSingleCol("v", "text"), []models.Row{
		{Values: []any{"abcdef\nline2\nline3\nline4\nline5"}},
		{Values: []any{"next"}},
	})
	v.SetViewMode(ViewModeExpanded)
	target := newTallTestView("motion", 4)
	v.Render(target)
	v.HorizScrollRight()
	v.Render(target)
	require.Contains(t, target.Buffer(), "| bcdef")
	require.NotContains(t, target.Buffer(), "| abcdef")
	v.HorizScrollLeft()
	v.HalfPageDown()
	v.Render(target)
	require.Equal(t, 2, v.snapshot().expandedLineOffset, "half of actual four-line viewport")
	require.Contains(t, target.Buffer(), "| line2")
	v.HalfPageUp()
	v.Render(target)
	require.Equal(t, 0, v.snapshot().expandedLineOffset)
	for _, move := range []func(){v.MoveCursorDown, v.MoveCursorUp, v.JumpLast, v.JumpFirst, func() { v.SetCursor(1, 0) }} {
		v.WrappedLineDown()
		v.HorizScrollRight()
		move()
		v.Render(target)
		require.Zero(t, v.snapshot().expandedLineOffset)
		require.Zero(t, v.snapshot().colOffset)
	}
	v.WrappedLineDown()
	v.HorizScrollRight()
	v.SetSearch("line5")
	v.Render(target)
	v.SetSearch("next")
	v.Render(target)
	require.Zero(t, v.snapshot().expandedLineOffset, "search record changes reset offsets")
	require.Zero(t, v.snapshot().colOffset)
}

// --- ialt.2: header reads configured TableHeader ---

func headerSnapForTheme(t *testing.T) (*View, viewSnapshot) {
	t.Helper()
	v := NewView()
	cols := make([]models.ColumnMeta, 0, 6)
	vals := make([]any, 0, 6)
	for i := range 6 {
		cols = append(cols, models.ColumnMeta{Name: fmt.Sprintf("c%d", i), TypeName: "text"})
		vals = append(vals, fmt.Sprintf("val%d_%s", i, strings.Repeat("x", 9)))
	}
	v.SetColumns(cols)
	v.AppendRows([]models.Row{{Values: vals}})
	v.widths = []int{14, 14, 14, 14, 14, 14}
	return v, v.snapshot()
}

// A configured TableHeader token wraps the whole header line.
func TestRenderHeaderLine_UsesConfiguredTableHeader(t *testing.T) {
	defer theme.SetMonochromeForTest(false)()
	cfg := builtin.DefaultDark()
	cfg.TableHeader = "color118"
	require.NoError(t, theme.Apply(cfg))
	t.Cleanup(func() { _ = theme.Apply(builtin.DefaultDark()) })

	_, snap := headerSnapForTheme(t)
	out := renderHeaderLine(snap, 80, false, false)

	require.True(t, strings.HasPrefix(out, "\x1b[38;5;118m"), "header must open with the configured SGR; got %q", out)
	require.True(t, strings.HasSuffix(out, ansiReset), "header must close with reset; got %q", out)
}

// The blocker guard: the SGR pair must be the OUTERMOST layer, applied AFTER
// the rune-index arrow overlay — otherwise overlayColumnArrows corrupts the
// escape bytes. With a ◄ arrow present, the configured escape must still be
// intact at the head of the line.
func TestRenderHeaderLine_ColorIntactWithArrowOverlay(t *testing.T) {
	defer theme.SetMonochromeForTest(false)()
	cfg := builtin.DefaultDark()
	cfg.TableHeader = "color118"
	require.NoError(t, theme.Apply(cfg))
	t.Cleanup(func() { _ = theme.Apply(builtin.DefaultDark()) })

	v, _ := headerSnapForTheme(t)
	v.JumpColLast()
	snap := v.snapshot()
	snap.rowOffset, snap.colOffset = v.clampOffsetsLocked(snap, 80, 24)
	left, right := columnScrollHints(snap, 80)

	out := renderHeaderLine(snap, 80, left, right)

	require.Contains(t, out, "◄", "scrolled-right header must carry the ◄ arrow")
	require.True(t, strings.HasPrefix(out, "\x1b[38;5;118m"),
		"escape must be intact at line head (proves SGR wrapped AFTER the arrow overlay); got %q", out)
	require.True(t, strings.HasSuffix(out, ansiReset), "header must close with reset; got %q", out)
}

func TestRenderHeaderLine_PlainUnderNoColor(t *testing.T) {
	defer theme.SetMonochromeForTest(true)()

	_, snap := headerSnapForTheme(t)
	out := renderHeaderLine(snap, 80, false, false)

	require.NotContains(t, out, "\x1b", "no escape under NO_COLOR; got %q", out)
}

func TestReview_width_header_arrows(t *testing.T) {
	for _, input := range []string{"中文测试", "e\u0301abcdef", "👩‍💻abcdef", "abcdef🚀", "中文", "abc", "ab", "a", ""} {
		for _, arrows := range [][2]bool{{true, false}, {false, true}, {true, true}, {false, false}} {
			out := overlayColumnArrows(input, arrows[0], arrows[1])
			width := runewidth.StringWidth(input)
			require.Equal(t, width, runewidth.StringWidth(out), "%q -> %q", input, out)
			if width < 2 {
				require.Equal(t, input, out)
				continue
			}
			if arrows[1] {
				pos := strings.Index(out, "►")
				require.NotEqual(t, -1, pos)
				require.Equal(t, width-2, runewidth.StringWidth(out[:pos]))
				require.True(t, strings.HasSuffix(out, " "))
			}
			if arrows[0] && (!arrows[1] || width >= 4) {
				require.True(t, strings.HasPrefix(out, " ◄"), "%q", out)
			}
		}
	}
	require.Equal(t, " ◄cd► ", overlayColumnArrows("abcdef", true, true))
}

func TestReview_width_header_body_hit_coordinates(t *testing.T) {
	v := NewView()
	v.SetColumns([]models.ColumnMeta{textCol("中文列"), textCol("e\u0301列"), textCol("🚀列")})
	v.AppendRows([]models.Row{{Values: []any{"中文测试", "e\u0301e\u0301", "🚀🚀"}}})
	v.widths = []int{8, 6, 6}
	const width = 26
	snap := v.snapshot()
	header := stripANSI(renderHeaderLine(snap, width, false, false))
	body := stripANSI(renderDataLine(snap, 0, width))
	require.Equal(t, width, runewidth.StringWidth(header))
	seps := []int{}
	for i, r := range header {
		if r == '│' {
			seps = append(seps, runewidth.StringWidth(header[:i]))
		}
	}
	require.Equal(t, []int{9, 18}, seps, "%q", header)
	// Body columns are separated by spaces, not the header's vertical bars.
	for _, tc := range []struct {
		value string
		x     int
	}{{"中文测试", 0}, {"e\u0301e\u0301", 11}, {"🚀🚀", 20}} {
		pos := strings.Index(body, tc.value)
		require.NotEqual(t, -1, pos)
		require.Equal(t, tc.x, runewidth.StringWidth(body[:pos]), "%q", body)
	}
	for x := 0; x < width; x++ {
		want := -1
		switch {
		case x < 8:
			want = 0
		case x >= 11 && x < 17:
			want = 1
		case x >= 20:
			want = 2
		}
		require.Equal(t, want, v.headerColumnAt(x), "cell %d", x)
	}
	for _, arrows := range [][2]bool{{true, false}, {false, true}, {true, true}} {
		line := stripANSI(renderHeaderLine(snap, width, arrows[0], arrows[1]))
		require.Equal(t, width, runewidth.StringWidth(line))
	}
}

func TestGridControlsKeepRowsAndRawValues(t *testing.T) {
	rows := []models.Row{
		{Values: []any{"before"}},
		{Values: []any{"A\nB\t界\r\x1b[31m"}},
		{Values: []any{"after"}},
		{Values: []any{"tail"}},
	}
	v := makeView(makeSingleCol("value", "text"), rows)
	target := gocui.NewView("controls", 0, 0, 61, 4, gocui.OutputNormal)
	target.Wrap = false
	for _, tc := range []struct {
		cursor int
		first  string
		last   string
		start  int
	}{
		{0, "before", `A\nB\t界`, 0},
		{2, `A\nB\t界`, "after", 1},
		{3, "after", "tail", 2},
		{0, "before", `A\nB\t界`, 0},
	} {
		v.SetCursor(tc.cursor, 0)
		v.Render(target)
		lines := target.ViewBufferLines()
		require.Len(t, lines, 3, "one header plus two database rows")
		require.Contains(t, lines[1], tc.first)
		require.Contains(t, lines[2], tc.last)
		require.Equal(t, rows[tc.start:tc.start+2], v.VisibleRows())
		value, _, _, _, ok := v.CellEditSnapshot()
		require.True(t, ok)
		require.Equal(t, rows[tc.cursor].Values[0], value, "viewer/editor must retain original controls")
	}
	require.Equal(t, rows, v.AllRows())
	require.Equal(t, []int{8}, v.snapshot().widths, "autosize must include visible escapes")
}

func TestGridControlTextMatchesRendering(t *testing.T) {
	const raw = "A\nhit\thit"
	v := makeView(makeSingleCol("value", "text"), []models.Row{{Values: []any{raw}}})
	v.SetSearch("hit")
	snap := v.snapshot()
	require.Equal(t, []cellMatch{{row: 0, col: 0, byteStart: 3, byteEnd: 6}, {row: 0, col: 0, byteStart: 8, byteEnd: 11}}, snap.searchMatches)
	line := renderDataLine(snap, 0, 80)
	require.Contains(t, line, curSearchSGR+"hit"+ansiReset)
	require.Contains(t, line, searchSGR+"hit"+ansiReset)
	require.Contains(t, stripANSI(line), `A\nhit\thit`)
	for _, typ := range []string{"text", "bytea"} {
		for _, dirty := range []bool{false, true} {
			padded := stripANSI(renderCellPadded("A\nB\tC\x1b[31m", models.ColumnMeta{TypeName: typ}, 50, dirty))
			require.NotContains(t, padded, "\n", typ)
			require.NotContains(t, padded, "\t", typ)
			require.Equal(t, `A\nB\tC`, strings.TrimSpace(padded), typ)
		}
	}
}

func TestGridHeaderPreservesCellText(t *testing.T) {
	for _, tc := range []struct{ name, display string }{
		{"cost|", "cost|"}, {"cost│", "cost│"}, {"val\nue\t", `val\nue\t`},
	} {
		v := makeView([]models.ColumnMeta{textCol(tc.name)}, nil)
		header := stripANSI(renderHeaderLine(v.snapshot(), 24, false, false))
		require.True(t, strings.HasPrefix(header, tc.display), "%q became %q", tc.name, header)
		require.Equal(t, tc.name, v.snapshot().cols[0].Name)
		require.NotContains(t, header, "\n")
		require.NotContains(t, header, "\t")
	}
}

func BenchmarkGridCellDisplay(b *testing.B) {
	for _, value := range []string{"ASCII text with a hit", "界⌛︎ hit\n👩‍💻\tvalue"} {
		b.Run(value, func(b *testing.B) {
			v := NewView()
			v.SetColumns([]models.ColumnMeta{textCol("first"), textCol("second"), textCol("third")})
			rows := make([]models.Row, 24)
			for i := range rows {
				rows[i] = models.Row{Values: []any{value, value, value}}
			}
			v.AppendRows(rows)
			v.SetSearch("hit")
			target := newTallTestView("benchmark", 24)
			b.ReportAllocs()
			for b.Loop() {
				v.Render(target)
			}
		})
	}
}
