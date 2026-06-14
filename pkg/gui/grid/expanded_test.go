package grid

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// makeView builds a *View pre-loaded with cols + rows for tests.
func makeView(cols []models.ColumnMeta, rows []models.Row) *View {
	v := NewView()
	v.SetColumns(cols)
	v.AppendRows(rows)
	return v
}

func TestView_SetViewMode_RoundTrips(t *testing.T) {
	v := NewView()
	require.Equal(t, ViewModeGrid, v.ViewMode(), "default mode is grid")
	v.SetViewMode(ViewModeExpanded)
	require.Equal(t, ViewModeExpanded, v.ViewMode())
	v.SetViewMode(ViewModeGrid)
	require.Equal(t, ViewModeGrid, v.ViewMode())
	// Unknown values fall back to grid (AC).
	v.SetViewMode("garbage")
	require.Equal(t, ViewModeGrid, v.ViewMode())
}

// TestRenderExpanded_FollowsProjectedCursorUnderFilter is a regression test
// for the follow-up: in expanded mode the displayed record is
// chosen from the cursor. cursorRow was redefined as a RAW-buffer
// index that navigation steps in projected order, so expanded mode must
// translate cursorRow through projectedPos. Updated so that
// applyFilter is now identity, so the projection is the full buffer and
// JumpFirst lands on raw row 0; expanded mode must show that record.
func TestRenderExpanded_FollowsProjectedCursor(t *testing.T) {
	v := NewView()
	v.SetColumns(makeSingleCol("c1", "text"))
	rows := make([]models.Row, 100)
	for i := range rows {
		rows[i] = models.Row{Values: []any{"rec-" + rowLabel(i)}}
	}
	v.AppendRows(rows)
	require.Equal(t, 0, projectIndices(v)[0], "precondition: identity projection, first record is raw row 0")
	v.SetViewMode(ViewModeExpanded)

	v.JumpFirst() // identity projection: lands on raw 0

	target := newTallTestView("expandfirst", 10)
	v.Render(target)
	buf := target.Buffer()

	require.Contains(t, buf, "rec-"+rowLabel(0),
		"expanded mode must show the projected-first record after JumpFirst")
}

func TestRenderExpanded_RendersRecordWithGutterAndValue(t *testing.T) {
	cols := []models.ColumnMeta{
		{Name: "id", TypeName: "int4"},
		{Name: "name", TypeName: "text"},
	}
	rows := []models.Row{{Values: []any{1, "alice"}}, {Values: []any{2, "bob"}}}
	v := makeView(cols, rows)
	v.SetViewMode(ViewModeExpanded)

	snap := v.snapshot()
	body := renderExpanded(snap, 80, 24)

	require.Contains(t, body, "[ RECORD 1")
	require.Contains(t, body, "id")
	require.Contains(t, body, "| 1")
	require.Contains(t, body, "name")
	require.Contains(t, body, "| alice")
}

func TestRenderExpanded_EmptyResult_RendersBanner(t *testing.T) {
	v := makeView([]models.ColumnMeta{{Name: "id", TypeName: "int4"}}, nil)
	v.SetViewMode(ViewModeExpanded)
	body := renderExpanded(v.snapshot(), 80, 24)
	require.Contains(t, body, "[ no records")
	// Must not panic / crash on toggling expanded on an empty grid.
}

func TestRenderExpanded_GutterClampedTo32(t *testing.T) {
	long := strings.Repeat("a", 50) // 50-char column name
	cols := []models.ColumnMeta{{Name: long, TypeName: "text"}}
	rows := []models.Row{{Values: []any{"v"}}}
	v := makeView(cols, rows)
	v.SetViewMode(ViewModeExpanded)
	body := renderExpanded(v.snapshot(), 200, 10)
	// Truncation rule: name displayed at <= 32 with ellipsis.
	// We look for "…" indicating truncation occurred.
	require.Contains(t, body, "…",
		"long column name should be truncated with ellipsis")
}

func TestRenderExpanded_GutterClampedTo12(t *testing.T) {
	cols := []models.ColumnMeta{{Name: "x", TypeName: "text"}}
	rows := []models.Row{{Values: []any{"v"}}}
	v := makeView(cols, rows)
	v.SetViewMode(ViewModeExpanded)
	body := renderExpanded(v.snapshot(), 80, 10)
	// Find a data line and check the gutter portion is at least 12 wide.
	lines := strings.Split(body, "\n")
	var dataLine string
	for _, ln := range lines {
		if strings.Contains(ln, "| v") {
			dataLine = ln
			break
		}
	}
	require.NotEmpty(t, dataLine)
	pipeIdx := strings.Index(dataLine, "|")
	require.GreaterOrEqual(t, pipeIdx, expandedGutterMin, "gutter must be >= 12 wide")
}

func TestRenderExpanded_LongValueWraps(t *testing.T) {
	cols := []models.ColumnMeta{{Name: "v", TypeName: "text"}}
	long := strings.Repeat("x", 200)
	rows := []models.Row{{Values: []any{long}}}
	v := makeView(cols, rows)
	v.SetViewMode(ViewModeExpanded)
	body := renderExpanded(v.snapshot(), 40, 24)
	// Multiple lines starting with the continuation gutter (spaces + "|")
	gutterCont := strings.Repeat(" ", expandedGutterMin) + " | "
	require.Contains(t, body, gutterCont,
		"long values should produce continuation lines")
}

func TestRenderExpanded_Overscan_DoesNotFormatAllRecords(t *testing.T) {
	// Build a large result and confirm renderExpanded only emits a
	// small number of record blocks rather than all of them.
	cols := []models.ColumnMeta{{Name: "id", TypeName: "int4"}}
	rows := make([]models.Row, 1000)
	for i := range rows {
		rows[i] = models.Row{Values: []any{i}}
	}
	v := makeView(cols, rows)
	v.SetViewMode(ViewModeExpanded)
	body := renderExpanded(v.snapshot(), 80, 24)
	count := strings.Count(body, "[ RECORD ")
	require.LessOrEqual(t, count, 5,
		"renderExpanded must not format all 1000 records on one Render (got %d blocks)", count)
}

func TestRenderExpanded_SingleRecord_RendersWithoutPanic(t *testing.T) {
	cols := []models.ColumnMeta{{Name: "id", TypeName: "int4"}}
	rows := []models.Row{{Values: []any{42}}}
	v := makeView(cols, rows)
	v.SetViewMode(ViewModeExpanded)
	require.NotPanics(t, func() {
		_ = renderExpanded(v.snapshot(), 80, 24)
	})
}

func TestRenderExpanded_EstimatedTotal_SurfaceInBanner(t *testing.T) {
	cols := []models.ColumnMeta{{Name: "id", TypeName: "int4"}}
	rows := []models.Row{{Values: []any{1}}}
	v := makeView(cols, rows)
	v.SetViewMode(ViewModeExpanded)
	v.SetEstimatedRowsLoader(func() int64 { return 1234567 })
	body := renderExpanded(v.snapshot(), 80, 10)
	require.Contains(t, body, "~1234567",
		"separator should surface the estimated total when known")
}

func TestRenderExpanded_NoEstimate_UsesProjectedCount(t *testing.T) {
	cols := []models.ColumnMeta{{Name: "id", TypeName: "int4"}}
	rows := []models.Row{{Values: []any{1}}, {Values: []any{2}}}
	v := makeView(cols, rows)
	v.SetViewMode(ViewModeExpanded)
	body := renderExpanded(v.snapshot(), 80, 10)
	require.Contains(t, body, "~2",
		"separator should fall back to projected count when no estimate is wired")
}

func TestRenderExpanded_SanitizesTerminalEscapes(t *testing.T) {
	// Cell value contains an OSC sequence (terminal-title hijack).
	cols := []models.ColumnMeta{{Name: "v", TypeName: "text"}}
	rows := []models.Row{{Values: []any{"safe\x1b]0;evil\x07suffix"}}}
	v := makeView(cols, rows)
	v.SetViewMode(ViewModeExpanded)
	body := renderExpanded(v.snapshot(), 80, 10)
	require.NotContains(t, body, "\x1b]",
		"OSC introducer must be stripped from rendered cell")
	require.NotContains(t, body, "\x07",
		"BEL must be stripped")
	require.Contains(t, body, "safesuffix",
		"visible content remains after stripping")
}

func TestYank_ExpandedMode_EmitsColValueLines(t *testing.T) {
	cols := []models.ColumnMeta{
		{Name: "id", TypeName: "int4"},
		{Name: "name", TypeName: "text"},
	}
	rows := []models.Row{{Values: []any{1, "alice"}}}
	v := makeView(cols, rows)
	v.SetViewMode(ViewModeExpanded)
	v.EnterRowMode()
	out := v.Yank()
	require.Equal(t, "id\t1\nname\talice\n", out,
		"expanded-mode yank produces col\\tvalue\\n lines per record")
}

func TestYank_GridMode_StillTSV(t *testing.T) {
	cols := []models.ColumnMeta{
		{Name: "id", TypeName: "int4"},
		{Name: "name", TypeName: "text"},
	}
	rows := []models.Row{{Values: []any{1, "alice"}}}
	v := makeView(cols, rows)
	v.EnterRowMode()
	out := v.Yank()
	require.Equal(t, "1\talice", out,
		"grid-mode yank stays in TSV format (1 row, 2 cols, no trailing newline)")
}

func TestEnterBlockMode_ExpandedFallsBackToRowMode(t *testing.T) {
	cols := []models.ColumnMeta{{Name: "id", TypeName: "int4"}}
	rows := []models.Row{{Values: []any{1}}}
	v := makeView(cols, rows)
	v.SetViewMode(ViewModeExpanded)
	v.EnterBlockMode()
	require.Equal(t, SelectionRow, v.SelectionMode(),
		"<C-v> in expanded mode must fall back to SelectionRow")
}

func TestEnterBlockMode_GridStaysBlock(t *testing.T) {
	cols := []models.ColumnMeta{{Name: "id", TypeName: "int4"}}
	rows := []models.Row{{Values: []any{1}}}
	v := makeView(cols, rows)
	v.EnterBlockMode()
	require.Equal(t, SelectionBlock, v.SelectionMode())
}

func TestWrappedLineDown_GridModeIsNoop(t *testing.T) {
	v := NewView()
	v.WrappedLineDown()
	// No panic, no state corruption. (No public accessor; just smoke-test.)
}

// TestTruncateToWidth_RuneBoundary asserts width-aware truncation never
// splits a multibyte rune and never exceeds the column budget. The AC
// edge case: "中文测试abc" to width 4 must yield valid UTF-8 on a rune
// boundary (中 + … = 2+1 = 3 cols) with width ≤ 4.
func TestTruncateToWidth_RuneBoundary(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		maxCols  int
		wantText string // exact expectation where deterministic
	}{
		{"cjk to 4", "中文测试abc", 4, "中…"},
		{"ascii fits", "abc", 8, "abc"},
		{"cjk fits exactly", "中文", 4, "中文"},
		{"emoji narrow", "😀😀😀", 3, "😀…"},
		{"combining", "éééé", 2, "é…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateToWidth(tc.in, tc.maxCols)
			require.True(t, utf8.ValidString(got),
				"truncated string must be valid UTF-8, got %q", got)
			require.LessOrEqual(t, runewidth.StringWidth(got), tc.maxCols,
				"display width of %q must not exceed %d", got, tc.maxCols)
			if tc.wantText != "" {
				require.Equal(t, tc.wantText, got)
			}
		})
	}
}

// TestWrapValue_WideRunesNeverSplit asserts the value-wrap path cuts on
// rune boundaries by display width, so CJK / emoji content wrapping at
// a narrow value column never corrupts a rune.
func TestWrapValue_WideRunesNeverSplit(t *testing.T) {
	// 5 CJK runes = 10 display cols, wrapped at width 4 (2 wide runes
	// per line). No line may exceed 4 cols and every chunk valid UTF-8.
	chunks := wrapValue("中文测试中", 4)
	for _, ch := range chunks {
		require.True(t, utf8.ValidString(ch),
			"wrapped chunk must be valid UTF-8, got %q", ch)
		require.LessOrEqual(t, runewidth.StringWidth(ch), 4,
			"wrapped chunk %q exceeds width budget", ch)
	}
	// Re-joining the chunks reproduces the original (no rune lost/added).
	require.Equal(t, "中文测试中", strings.Join(chunks, ""))
}

// TestExpandedRecordLines_WideName asserts a CJK column name longer than
// the gutter is truncated on a rune boundary with the ellipsis and the
// gutter padding lands the pipe at the gutter column.
func TestExpandedRecordLines_WideName(t *testing.T) {
	name := strings.Repeat("名", 30) // 60 display cols, well over gutter
	lines := expandedRecordLines(name, "v", expandedGutterMax, 40)
	require.NotEmpty(t, lines)
	first := lines[0]
	require.True(t, utf8.ValidString(first),
		"record line must be valid UTF-8, got %q", first)
	pipeIdx := strings.Index(first, "|")
	require.GreaterOrEqual(t, pipeIdx, 0)
	gutterPart := first[:pipeIdx]
	require.Equal(t, expandedGutterMax, runewidth.StringWidth(gutterPart)-1,
		"gutter content (excluding trailing space before pipe) should fill the gutter width")
}

func TestRender_DispatchesOnViewMode(t *testing.T) {
	cols := []models.ColumnMeta{{Name: "id", TypeName: "int4"}}
	rows := []models.Row{{Values: []any{42}}}
	v := makeView(cols, rows)
	// Grid mode renders as a row with the column header.
	v.SetViewMode(ViewModeGrid)
	bodyGrid := renderBody(v.snapshot(), 80, 24)
	require.Contains(t, bodyGrid, "id")
	require.NotContains(t, bodyGrid, "[ RECORD ")
	// Expanded mode produces a RECORD banner.
	v.SetViewMode(ViewModeExpanded)
	bodyExp := renderExpanded(v.snapshot(), 80, 24)
	require.Contains(t, bodyExp, "[ RECORD ")
}
