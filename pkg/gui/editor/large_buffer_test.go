package editor

import (
	"strings"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/stretchr/testify/require"
)

func TestLargeBufferViewOnlyMaterializesVisibleRows(t *testing.T) {
	buf := largeBenchmarkBuffer(strings.Repeat("SELECT 1;\n", 10000))
	buf.SetCursor(Position{Line: 5000, Col: 3})
	v := gocui.NewView("test", 0, 0, 100, 24, gocui.OutputNormal)
	syncViewToBuffer(v, buf)
	require.Equal(t, 10001, v.LinesHeight())
	lines := v.BufferLines()
	require.Empty(t, lines[0], "off-screen rows must not be reparsed into gocui cells")
	require.Equal(t, "SELECT 1;", strings.TrimRight(lines[5000], " "))
	require.Equal(t, "SELECT 1;", buf.TextInRange(Range{Start: Position{}, End: Position{Col: 9}}), "view virtualization must not discard canonical text")
}

func TestLargeDocumentBudget(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want bool
	}{
		{"at limit", strings.Repeat("界", InteractiveRuneLimit), false},
		{"over limit", strings.Repeat("界", InteractiveRuneLimit+1), true},
		{"newlines count", strings.Repeat("\n", InteractiveRuneLimit+1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, largeBenchmarkBuffer(tc.text).IsLargeDocument())
		})
	}
}

func TestLargeBufferViewFollowsEditsAndUndo(t *testing.T) {
	buf := largeBenchmarkBuffer(strings.Repeat("SELECT 1;\n", 10000))
	v := gocui.NewView("test", 0, 0, 100, 24, gocui.OutputNormal)
	for _, row := range []int{0, 5000, 10000, 7} {
		buf.SetCursor(Position{Line: row})
		syncViewToBuffer(v, buf)
		_, origin := v.Origin()
		require.LessOrEqual(t, origin, row)
		require.Less(t, row, origin+v.InnerHeight())
		_, cy := v.Cursor()
		require.Equal(t, row, origin+cy)
		require.Equal(t, buf.LineCount(), v.LinesHeight())
	}
	cur := buf.CursorPos()
	require.NoError(t, buf.Apply(Edit{Kind: EditKindInsert, Range: Range{Start: cur, End: cur}, Text: "x\n"}))
	buf.SetCursor(Position{Line: cur.Line + 1})
	syncViewToBuffer(v, buf)
	require.Equal(t, buf.LineCount(), v.LinesHeight())
	require.Equal(t, "x", strings.TrimRight(v.BufferLines()[cur.Line], " "))
	require.NoError(t, buf.Undo())
	syncViewToBuffer(v, buf)
	require.Equal(t, buf.LineCount(), v.LinesHeight())
	require.Equal(t, "SELECT 1;", strings.TrimRight(v.BufferLines()[cur.Line], " "))
}

func TestLargeBufferViewRebasesOverlays(t *testing.T) {
	buf := largeBenchmarkBuffer(strings.Repeat("SELECT 1;\n", 10000))
	buf.SetCursor(Position{Line: 5000, Col: 3})
	sel := Range{Start: Position{Line: 4990}, End: Position{Line: 5010, Col: 4}}
	buf.Selection = &sel
	buf.SetYankFlash(Range{Start: Position{Line: 5000}, End: Position{Line: 5000, Col: 6}})
	content, start, _, _, large, selection, flash := buf.viewSnapshot(0, 24)
	require.True(t, large)
	require.Equal(t, 4988, start)
	require.Equal(t, sel.Start.Line-start, selection.Start.Line)
	require.Equal(t, 5000-start, flash.Start.Line)
	require.Contains(t, ApplySelectionOverlay(content, *selection), ansiSelBgOn)
	require.Contains(t, ApplyYankFlashOverlay(content, *flash), ansiYankBgOn)
}

func TestLargeBufferViewRepeatedEOFPaints(t *testing.T) {
	buf := largeBenchmarkBuffer(strings.Repeat("SELECT 1;\n", 10000))
	buf.SetCursor(Position{Line: 10000})
	v := gocui.NewView("test", 0, 0, 100, 24, gocui.OutputNormal)
	syncViewToBuffer(v, buf)
	v.Editable = true
	for range 3 {
		syncViewToBuffer(v, buf)
		require.Equal(t, buf.LineCount(), v.LinesHeight())
		require.Equal(t, "SELECT 1;", strings.TrimRight(v.BufferLines()[9999], " "))
	}
}
