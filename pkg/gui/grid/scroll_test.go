package grid

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/dbsavvy/pkg/models"
)

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
	for i := 0; i < 10; i++ {
		v.MoveCursorDown()
	}
	r, _ = v.CursorPosition()
	require.Equal(t, 1, r, "cursor row must clamp to len(rows)-1")

	// MoveCursorRight past last col stays at last col.
	for i := 0; i < 10; i++ {
		v.MoveCursorRight()
	}
	_, c = v.CursorPosition()
	require.Equal(t, 1, c, "cursor col must clamp to len(cols)-1")
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

	for i := 0; i < 50; i++ {
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
	for i := 0; i < 4; i++ {
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

	var fires int64
	v.SetOnNearTail(func(n int) {
		atomic.AddInt64(&fires, 1)
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
	for i := 0; i < 28; i++ {
		v.MoveCursorDown()
		v.Render(target)
	}
	require.Equal(t, int64(1), atomic.LoadInt64(&fires),
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
	require.Equal(t, int64(2), atomic.LoadInt64(&fires),
		"onNearTail should re-fire after the buffer grows past the last fire point")
}

// TestJumpLast_LandsAtLastLoadedRow pins the grid-side cursor jump that
// ]p (Page(+1)) relies on. dbsavvy-uv0.3 AC #2.
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
// dbsavvy-uv0.3 AC #2.
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
	for _, line := range strings.Split(s, "\n") {
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

// TestRenderDataLine_DirtyCellShowsStagedValue verifies that once a result
// grid is wired to a PendingEditSet (SetPendingEdits) and has a row
// identity, renderDataLine renders a staged edit's NewValue — not the
// stale DB value — with the dirty marker. This is the integration the A3
// feature was missing (dbsavvy-cyh): the staged set must reach the render
// snapshot and be looked up per cell by PK + column.
func TestRenderDataLine_DirtyCellShowsStagedValue(t *testing.T) {
	v := NewView()
	v.SetColumns([]models.ColumnMeta{
		{Name: "id", TypeName: "int4"},
		{Name: "name", TypeName: "text"},
	})
	v.AppendRows([]models.Row{
		{Values: []any{1, "alice"}},
		{Values: []any{2, "carol"}},
	})
	// Row identity = column 0 (id); editable result.
	v.SetEditability(true, []int{0}, "", "public")

	set := &models.PendingEditSet{}
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
	if !strings.Contains(line0, "●") {
		t.Errorf("dirty cell must carry the ● marker; line=%q", line0)
	}
	if strings.Contains(line0, "alice") {
		t.Errorf("dirty cell must NOT render the stale value 'alice'; line=%q", line0)
	}

	// Row 1 (id=2): no staged edit — original value, no marker.
	line1 := renderDataLine(snap, 1, 80)
	if !strings.Contains(line1, "carol") {
		t.Errorf("clean row must render original value 'carol'; line=%q", line1)
	}
	if strings.Contains(line1, "●") {
		t.Errorf("clean row must not carry a dirty marker; line=%q", line1)
	}
}

// projectedView builds a single text-column view with the given label
// values in raw (insertion) order. Used by the projected-cursor navigation
// regression tests, which drive a non-contiguous projection via an active
// FILTER (dbsavvy-72k.6: the grid no longer reorders for sort).
func projectedView(t *testing.T, labels ...string) *View {
	t.Helper()
	v := NewView()
	v.SetColumns([]models.ColumnMeta{{Name: "name", TypeName: "text"}})
	for _, l := range labels {
		v.AppendRows([]models.Row{{Values: []any{l}}})
	}
	return v
}

// TestCursorNavigation_FollowsProjectedOrderUnderFilter is a regression test
// for dbsavvy-dr6: with a filter active the cursor must walk the projected
// (visible) row order, not the raw buffer order. The filter "^m" matches
// only every other row, so the projected list has gaps: raw [match, skip,
// match, skip, match] → projected raw indices [0, 2, 4]. JumpFirst/Last and
// j/k must address those raw rows in projected order.
func TestCursorNavigation_FollowsProjectedOrderUnderFilter(t *testing.T) {
	// Raw order alternates match/no-match so the projection is non-contiguous.
	v := projectedView(t, "match-0", "skip-1", "match-2", "skip-3", "match-4")
	require.NoError(t, v.SetFilter("^match", false))
	require.Equal(t, []int{0, 2, 4}, projectIndices(v), "precondition: projected order")

	// JumpFirst lands on the first PROJECTED row (raw 0).
	v.JumpFirst()
	r, _ := v.CursorPosition()
	require.Equal(t, 0, r, "JumpFirst must land on the first PROJECTED row")

	// Down walks projected order: raw 0 -> 2 -> 4 (skipping filtered rows),
	// then clamps.
	v.MoveCursorDown()
	r, _ = v.CursorPosition()
	require.Equal(t, 2, r, "MoveCursorDown must skip the filtered-out raw row 1")
	v.MoveCursorDown()
	r, _ = v.CursorPosition()
	require.Equal(t, 4, r, "MoveCursorDown must skip the filtered-out raw row 3")
	v.MoveCursorDown() // clamp at projected tail
	r, _ = v.CursorPosition()
	require.Equal(t, 4, r, "MoveCursorDown must clamp at the last PROJECTED row")

	// JumpLast lands on the last PROJECTED row (raw 4).
	v.JumpLast()
	r, _ = v.CursorPosition()
	require.Equal(t, 4, r, "JumpLast must land on the last PROJECTED row")

	// Up walks projected order backwards: raw 4 -> 2 -> 0, then clamps.
	v.MoveCursorUp()
	r, _ = v.CursorPosition()
	require.Equal(t, 2, r)
	v.MoveCursorUp()
	r, _ = v.CursorPosition()
	require.Equal(t, 0, r)
	v.MoveCursorUp() // clamp at projected head
	r, _ = v.CursorPosition()
	require.Equal(t, 0, r, "MoveCursorUp must clamp at the first PROJECTED row")
}

// TestRender_VerticalScroll_FollowsCursorUnderFilter is the dbsavvy-dr6
// viewport half: after JumpLast under an active filter that subsets the
// buffer, the viewport must scroll so the cursor's row is on-screen. The
// bug scrolled to the raw-index tail, leaving the cursor row off-screen.
// Only every 10th row matches, so the projected tail is a row well before
// the raw tail.
func TestRender_VerticalScroll_FollowsCursorUnderFilter(t *testing.T) {
	v := NewView()
	v.SetColumns(makeSingleCol("c1", "text"))
	rows := make([]models.Row, 100)
	for i := range rows {
		// Every 10th row matches the filter; the rest are non-matching.
		if i%10 == 0 {
			rows[i] = models.Row{Values: []any{"match-" + rowLabel(i)}}
		} else {
			rows[i] = models.Row{Values: []any{rowLabel(i)}}
		}
	}
	v.AppendRows(rows)
	require.NoError(t, v.SetFilter("^match", false))

	v.JumpLast() // last PROJECTED row == raw 90 (the last matching row)
	r, _ := v.CursorPosition()
	require.Equal(t, 90, r, "JumpLast lands on the last matching raw row, not the raw tail")

	target := newTallTestView("filterscroll", 10)
	v.Render(target)
	buf := target.Buffer()

	require.Contains(t, buf, "match-"+rowLabel(r),
		"cursor row must be on-screen after JumpLast under a filter")
}
