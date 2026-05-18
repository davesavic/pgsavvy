package grid

import (
	"strings"
	"sync"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// newTestView returns a gocui.View sized to give a usable InnerSize the
// grid renderer can use. InnerSize is (x1-x0+1)-2 by (y1-y0+1)-2, so
// (0,0)-(81,25) yields 80x24 — matches the Render fallback constants.
func newTestView(name string) *gocui.View {
	return gocui.NewView(name, 0, 0, 81, 25, gocui.OutputNormal)
}

// makeSingleCol builds a 1-column schema with the given name + type.
func makeSingleCol(name, typ string) []models.ColumnMeta {
	return []models.ColumnMeta{{Name: name, TypeName: typ}}
}

// TestAppendRows_ConcurrentSafe verifies that many concurrent AppendRows
// calls don't lose rows or race on the row buffer. Run with -race.
func TestAppendRows_ConcurrentSafe(t *testing.T) {
	v := NewView()
	v.SetColumns(makeSingleCol("c1", "text"))

	const goroutines = 10
	const perGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			batch := make([]models.Row, perGoroutine)
			for i := range batch {
				batch[i] = models.Row{Values: []any{"x"}}
			}
			v.AppendRows(batch)
		}()
	}
	wg.Wait()

	require.Equal(t, goroutines*perGoroutine, v.RowCount(),
		"all concurrently appended rows should be present")
}

// TestSetColumns_ResetsCursorAndSelection installs columns, moves the
// cursor + enters cell-mode selection, then re-installs columns and
// verifies that the cursor is back at (0,0) and the selection cleared
// via the observable Yank() output.
func TestSetColumns_ResetsCursorAndSelection(t *testing.T) {
	v := NewView()
	cols := []models.ColumnMeta{
		{Name: "a", TypeName: "text"},
		{Name: "b", TypeName: "text"},
	}
	v.SetColumns(cols)
	v.AppendRows([]models.Row{
		{Values: []any{"a0", "b0"}},
		{Values: []any{"a1", "b1"}},
	})

	v.MoveCursorDown()
	v.MoveCursorRight()
	v.EnterCellMode()
	require.Equal(t, SelectionCell, v.SelectionMode())

	// Re-install the same columns; this must reset cursor + selection.
	v.SetColumns(cols)
	require.Equal(t, SelectionNone, v.SelectionMode(),
		"SetColumns should clear selection mode")
	row, col := v.CursorPosition()
	require.Equal(t, 0, row, "cursor row must reset to 0")
	require.Equal(t, 0, col, "cursor col must reset to 0")

	// And Yank against fresh rows should fall back to (0,0) cell.
	v.AppendRows([]models.Row{
		{Values: []any{"fresh-a0", "fresh-b0"}},
	})
	require.Equal(t, "fresh-a0", v.Yank(),
		"Yank with no selection at (0,0) should return the (0,0) cell")
}

// TestRender_EmptyShowsZeroRows verifies the zero-state path. With no
// columns configured at all, the EmptyResultIndicator is emitted. With
// one column and zero rows, Render must not panic.
func TestRender_EmptyShowsZeroRows(t *testing.T) {
	v := NewView()
	target := newTestView("empty")
	// Zero columns → EmptyResultIndicator path.
	require.NotPanics(t, func() { v.Render(target) })
	require.Contains(t, target.Buffer(), "0 rows",
		"empty grid should mention a zero-row indicator")

	// One column, zero rows: just shouldn't panic.
	v2 := NewView()
	v2.SetColumns(makeSingleCol("c1", "text"))
	target2 := newTestView("empty2")
	require.NotPanics(t, func() { v2.Render(target2) })
}

// TestRender_HonoursTitle verifies SetTitle propagates into the target
// gocui.View's Title field during Render.
func TestRender_HonoursTitle(t *testing.T) {
	v := NewView()
	v.SetColumns(makeSingleCol("c1", "text"))
	v.SetTitle("hello")
	require.Equal(t, "hello", v.Title())

	target := newTestView("titled")
	v.Render(target)
	require.True(t, strings.Contains(target.Title, "hello"),
		"target gocui view Title should contain 'hello', got %q", target.Title)
}
