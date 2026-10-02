package grid

import (
	"strings"
	"sync"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/pgsavvy/pkg/models"
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
	for range goroutines {
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

// TestRender_EmptyWithRowsAffected verifies that a DML statement with no
// result set (zero columns) but a non-zero affected-row count surfaces the
// affected count in the body instead of the misleading "(0 rows)". Singular
// vs plural is honoured.
func TestRender_EmptyWithRowsAffected(t *testing.T) {
	v := NewView()
	v.SetRowsAffected(1)
	target := newTestView("dml-one")
	v.Render(target)
	require.Contains(t, target.Buffer(), "(1 row affected)",
		"single affected row should read singular")
	require.NotContains(t, target.Buffer(), "0 rows",
		"affected DML must not show the zero-row indicator")

	v2 := NewView()
	v2.SetRowsAffected(5)
	target2 := newTestView("dml-many")
	v2.Render(target2)
	require.Contains(t, target2.Buffer(), "(5 rows affected)",
		"multiple affected rows should read plural")
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
// expects VisibleRows to return rows[20:30].
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
// panicking.
func TestVisibleRows_EmptyBeforeRender(t *testing.T) {
	v := NewView()
	v.SetColumns(makeSingleCol("c1", "text"))
	v.AppendRows([]models.Row{{Values: []any{"a"}}})
	require.Empty(t, v.VisibleRows(), "VisibleRows before any Render must be empty")
}

func TestReview_expanded_visible_records(t *testing.T) {
	cols := []models.ColumnMeta{{Name: "id", TypeName: "int4"}, {Name: "body", TypeName: "text"}}
	rows := []models.Row{{Values: []any{3, "third"}}, {Values: []any{1, "first\nwrapped"}}, {Values: []any{2, "second"}}}
	v := makeView(cols, rows)
	v.SetViewMode(ViewModeExpanded)
	v.SetHiddenCols(map[int]bool{1: true})
	v.SetSortIndicator(0, SortAsc) // Ordering is supplied by the database, not the grid.
	v.SetCursor(1, 0)
	require.Empty(t, v.VisibleRows(), "no viewport before render")
	for _, tc := range []struct {
		height int
		want   []models.Row
	}{
		{1, rows[1:2]}, // Separator-only partial record qualifies.
		{2, rows[1:2]},
		{3, rows[1:3]}, // Following separator qualifies, but raw row 0 is offscreen.
		{4, rows[1:3]},
	} {
		t.Run(rowLabel(tc.height), func(t *testing.T) {
			target := newTallTestView("visible", tc.height)
			v.Render(target)
			require.Equal(t, tc.want, v.VisibleRows())
			require.NotContains(t, target.Buffer(), "third")
			require.NotContains(t, target.Buffer(), "wrapped", "hidden column changes block height")
			got := v.VisibleRows()
			got[0] = models.Row{}
			require.Equal(t, tc.want, v.VisibleRows(), "returned row slice is defensive")
		})
	}
	v.SetColumns(cols)
	require.Empty(t, v.VisibleRows(), "replacement invalidates the previous viewport")
	require.Zero(t, v.snapshot().expandedLineOffset)
}

func TestReview_expanded_zero_native_viewport(t *testing.T) {
	for _, tc := range []struct {
		name          string
		width, height int
	}{
		{"zero_width", 0, 4},
		{"zero_height", 40, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := makeView(makeSingleCol("v", "text"), []models.Row{{Values: []any{"visible"}}})
			v.SetViewMode(ViewModeExpanded)
			v.Render(newTestView("before"))
			require.Len(t, v.VisibleRows(), 1)
			target := gocui.NewView(tc.name, 0, 0, tc.width+1, tc.height+1, gocui.OutputNormal)
			v.Render(target)
			require.Empty(t, target.Buffer())
			require.Empty(t, v.VisibleRows())
		})
	}
}

func TestReview_expanded_empty_native_viewport(t *testing.T) {
	for _, tc := range []struct {
		width, height int
		want          string
	}{
		{0, 4, ""}, {40, 0, ""}, {2, 4, "(5"}, {40, 4, "(5 rows affected)"},
	} {
		t.Run(rowLabel(tc.width)+rowLabel(tc.height), func(t *testing.T) {
			v := NewView()
			v.SetRowsAffected(5)
			v.SetViewMode(ViewModeExpanded)
			target := gocui.NewView("empty", 0, 0, tc.width+1, tc.height+1, gocui.OutputNormal)
			v.Render(target)
			require.Equal(t, tc.want, strings.TrimRight(target.Buffer(), "\n"))
			require.Empty(t, v.VisibleRows())
		})
	}
}

// TestEditability_DefaultsAndSetAndReset verifies the SetEditability /
// Editable / RowIdentity / DisabledReason surface, including the defensive
// copy behaviour on the RowIdentity getter and the SetColumns reset.
func TestEditability_DefaultsAndSetAndReset(t *testing.T) {
	v := NewView()
	// Defaults.
	require.False(t, v.Editable(), "default Editable must be false")
	require.Nil(t, v.RowIdentity(), "default RowIdentity must be nil")
	require.Equal(t, "", v.DisabledReason(), "default DisabledReason must be empty")
	require.Equal(t, "", v.IdentitySchema(), "default IdentitySchema must be empty")

	// Install columns then populate editability.
	v.SetColumns(makeSingleCol("c1", "text"))
	v.SetEditability(true, []int{0, 2}, "", models.Ref{Schema: "myschema", Table: "t"})
	require.True(t, v.Editable())
	require.Equal(t, []int{0, 2}, v.RowIdentity())
	require.Equal(t, "", v.DisabledReason())
	require.Equal(t, "myschema", v.IdentitySchema(),
		"IdentitySchema must carry the catalog-resolved schema")

	// RowIdentity getter must return a defensive copy.
	got := v.RowIdentity()
	got[0] = 99
	require.Equal(t, []int{0, 2}, v.RowIdentity(),
		"mutating the returned RowIdentity slice must not affect View state")

	// SetColumns clears editability fields back to zero.
	v.SetColumns(makeSingleCol("c1", "text"))
	require.False(t, v.Editable(), "SetColumns must reset Editable")
	require.Nil(t, v.RowIdentity(), "SetColumns must reset RowIdentity")
	require.Equal(t, "", v.DisabledReason(), "SetColumns must reset DisabledReason")
	require.Equal(t, "", v.IdentitySchema(), "SetColumns must reset IdentitySchema")

	// Disabled-reason path: SetEditability with editable=false + reason.
	v.SetEditability(false, nil, "no row identity", models.Ref{})
	require.False(t, v.Editable())
	require.Nil(t, v.RowIdentity())
	require.Equal(t, "no row identity", v.DisabledReason())
}

func TestReview_target_grid_replacement(t *testing.T) {
	v := NewView()
	v.SetColumns(makeSingleCol("id", "int8"))
	ref := models.Ref{Schema: "a.b", Table: "t.u"}
	v.SetEditability(true, []int{0}, "", ref)
	require.Equal(t, ref, v.IdentityRef())
	v.SetPendingEdits(&models.PendingEditSet{Table: models.Ref{Schema: "a.b", Table: "t.u"}})
	v.SetColumns(nil)
	require.False(t, v.Editable())
	require.Empty(t, v.IdentitySchema())
	require.Equal(t, models.Ref{}, v.IdentityRef())
	require.Nil(t, v.PendingEdits(), "replacement must detach the prior relation's pending set")
}

// TestAllRows_ConcurrentSafety runs a producer goroutine appending rows
// alongside a reader goroutine repeatedly calling AllRows. Run with
// -race to catch any data race.
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

func TestView_SetYankFormat_ValidValues(t *testing.T) {
	for _, f := range []string{"json", "tsv", "csv", "ndjson"} {
		v := NewView()
		v.SetYankFormat(f)
		if got := v.YankFormat(); got != f {
			t.Errorf("YankFormat() = %q after SetYankFormat(%q), want %q", got, f, f)
		}
	}
}

func TestView_SetYankFormat_DefaultsToTSV(t *testing.T) {
	v := NewView()
	if got := v.YankFormat(); got != "tsv" {
		t.Errorf("default YankFormat() = %q, want \"tsv\"", got)
	}
}

func TestView_SetYankFormat_NormalisesUnknown(t *testing.T) {
	for _, f := range []string{"", "unknown", "JSON", "xml"} {
		v := NewView()
		v.SetYankFormat("json")
		v.SetYankFormat(f)
		if got := v.YankFormat(); got != "tsv" {
			t.Errorf("YankFormat() = %q after SetYankFormat(%q), want \"tsv\"", got, f)
		}
	}
}
