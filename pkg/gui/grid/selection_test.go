package grid

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// makeCanonical3x3 builds the standard "a..i" 3x3 grid used by the
// selection / yank tests.
func makeCanonical3x3(t *testing.T) *View {
	t.Helper()
	v := NewView()
	v.SetColumns([]models.ColumnMeta{
		{Name: "c0", TypeName: "text"},
		{Name: "c1", TypeName: "text"},
		{Name: "c2", TypeName: "text"},
	})
	v.AppendRows([]models.Row{
		{Values: []any{"a", "b", "c"}},
		{Values: []any{"d", "e", "f"}},
		{Values: []any{"g", "h", "i"}},
	})
	return v
}

// TestEnterRowMode_FullRowYank: with cursor on row 1 + EnterRowMode,
// Yank produces the entire middle row "d\te\tf".
func TestEnterRowMode_FullRowYank(t *testing.T) {
	v := makeCanonical3x3(t)
	v.MoveCursorDown() // to row 1
	v.EnterRowMode()
	require.Equal(t, "d\te\tf", v.Yank())
}

// TestEnterBlockMode_RectangleBounds: anchor at (0,0), grow cursor to
// (1,1), Yank produces the 2x2 top-left rectangle.
func TestEnterBlockMode_RectangleBounds(t *testing.T) {
	v := makeCanonical3x3(t)
	// Cursor starts at (0,0). EnterBlockMode anchors there.
	v.EnterBlockMode()
	v.MoveCursorDown()  // (1,0)
	v.MoveCursorRight() // (1,1)
	require.Equal(t, "a\tb\nd\te", v.Yank())
}

// TestSelectionRange_OrderingAnchorBehindCursor: enter cell mode at
// (2,2) and walk the cursor backwards to (0,0). In cell mode the
// selection range orders anchor/cursor naturally, so the visible range
// covers everything between (0,0) and (2,2). Yank in cell mode yields
// the cell at (0,0) ("a") through (2,2) ("i") block.
func TestSelectionRange_OrderingAnchorBehindCursor(t *testing.T) {
	v := makeCanonical3x3(t)
	// Move cursor to (2,2).
	v.MoveCursorDown()
	v.MoveCursorDown()
	v.MoveCursorRight()
	v.MoveCursorRight()
	v.EnterCellMode()
	// Now walk cursor back to (0,0).
	v.MoveCursorUp()
	v.MoveCursorUp()
	v.MoveCursorLeft()
	v.MoveCursorLeft()

	// orderInts swaps the bounds, so the selection range is (0,0)-(2,2).
	// Cell-mode Yank uses the rectangular range — same as block mode.
	require.Equal(t, "a\tb\tc\nd\te\tf\ng\th\ti", v.Yank(),
		"anchor-behind-cursor must produce an ordered rectangle")
}

// TestClearSelection_FallsBackToCursorCell: after ClearSelection, Yank
// collapses to the single cell under the cursor.
func TestClearSelection_FallsBackToCursorCell(t *testing.T) {
	v := makeCanonical3x3(t)
	v.MoveCursorDown()  // row 1
	v.MoveCursorRight() // col 1 → cursor at (1,1) → "e"
	v.EnterCellMode()
	v.ClearSelection()
	require.Equal(t, SelectionNone, v.SelectionMode())
	require.Equal(t, "e", v.Yank(),
		"after ClearSelection Yank should return the cell under the cursor")
}
