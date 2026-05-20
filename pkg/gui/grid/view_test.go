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

// TestAllRows_ReturnsCopyOfBufferedRows verifies AllRows returns every
// buffered row and that the result is a defensive copy — mutating the
// returned slice must not affect later AllRows calls or RowCount.
// dbsavvy-uv0.9.
func TestAllRows_ReturnsCopyOfBufferedRows(t *testing.T) {
	v := NewView()
	v.SetColumns(makeSingleCol("c1", "text"))
	batch := make([]models.Row, 10)
	for i := range batch {
		batch[i] = models.Row{Values: []any{i}}
	}
	v.AppendRows(batch)

	got := v.AllRows()
	require.Len(t, got, 10, "AllRows should return every buffered row")

	// Mutate the returned slice header — clearing it must not affect
	// the view's buffered rows.
	for i := range got {
		got[i] = models.Row{Values: []any{"clobbered"}}
	}
	require.Equal(t, 10, v.RowCount(), "RowCount must be unaffected by caller mutation")

	again := v.AllRows()
	require.Len(t, again, 10, "AllRows must return the original buffered rows")
	require.Equal(t, 0, again[0].Values[0], "row 0 value must be the original 0, not 'clobbered'")
}

// TestVisibleRows_ReturnsViewportSlice seeds the View with 100 rows and
// stamps viewport state (rowOffset=20, viewHeight=10) directly, then
// expects VisibleRows to return rows[20:30]. dbsavvy-uv0.9.
func TestVisibleRows_ReturnsViewportSlice(t *testing.T) {
	v := NewView()
	v.SetColumns(makeSingleCol("c1", "text"))
	batch := make([]models.Row, 100)
	for i := range batch {
		batch[i] = models.Row{Values: []any{i}}
	}
	v.AppendRows(batch)

	v.mu.Lock()
	v.rowOffset = 20
	v.viewHeight = 10
	v.mu.Unlock()

	got := v.VisibleRows()
	require.Len(t, got, 10, "viewport top=20 height=10 should yield 10 rows")
	require.Equal(t, 20, got[0].Values[0], "first visible row should be index 20")
	require.Equal(t, 29, got[9].Values[0], "last visible row should be index 29")
}

// TestVisibleRows_EmptyBeforeRender verifies that with no Render having
// run (viewHeight == 0) VisibleRows returns an empty slice rather than
// panicking. dbsavvy-uv0.9.
func TestVisibleRows_EmptyBeforeRender(t *testing.T) {
	v := NewView()
	v.SetColumns(makeSingleCol("c1", "text"))
	v.AppendRows([]models.Row{{Values: []any{"a"}}})
	require.Empty(t, v.VisibleRows(), "VisibleRows before any Render must be empty")
}

// TestAllRows_ConcurrentSafety runs a producer goroutine appending rows
// alongside a reader goroutine repeatedly calling AllRows. Run with
// -race to catch any data race. dbsavvy-uv0.9.
func TestAllRows_ConcurrentSafety(t *testing.T) {
	v := NewView()
	v.SetColumns(makeSingleCol("c1", "text"))

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := range 500 {
			v.AppendRows([]models.Row{{Values: []any{i}}})
		}
		close(done)
	}()

	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				_ = v.AllRows() // one final read after producer is done
				return
			default:
				_ = v.AllRows()
			}
		}
	}()

	wg.Wait()
	require.Equal(t, 500, v.RowCount(), "all producer appends must be present")
}
