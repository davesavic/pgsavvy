package grid

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// sortableView builds a view with two columns (name:text, age:int4) and
// the supplied rows installed. Used by the sort-comparator tests.
func sortableView(t *testing.T, rows [][]any) *View {
	t.Helper()
	v := NewView()
	v.SetColumns([]models.ColumnMeta{
		{Name: "name", TypeName: "text"},
		{Name: "age", TypeName: "int4"},
	})
	for _, r := range rows {
		v.AppendRows([]models.Row{{Values: r}})
	}
	return v
}

// projectIndices is a test helper that snapshots the view and runs the
// projection pipeline, returning the rendered row order.
func projectIndices(v *View) []int {
	snap := v.snapshot()
	return project(snap)
}

// TestSetSort_AscDescClearCycle pins the AC: first SetSort = asc, second
// on same col = desc, third = clear.
func TestSetSort_AscDescClearCycle(t *testing.T) {
	v := sortableView(t, nil)
	require.False(t, v.SortActive(), "fresh view should not have an active sort")

	v.SetSort(0)
	require.True(t, v.SortActive())
	v.mu.RLock()
	require.Equal(t, SortAsc, v.sortState.dir)
	require.Equal(t, 0, v.sortState.col)
	v.mu.RUnlock()

	v.SetSort(0)
	v.mu.RLock()
	require.Equal(t, SortDesc, v.sortState.dir)
	v.mu.RUnlock()

	v.SetSort(0)
	require.False(t, v.SortActive(), "third SetSort on same col should clear")
}

// TestSetSort_DifferentColumnRestartsAsc pins: SetSort on a different
// column restarts the cycle in ascending order, even if the previous
// column was descending.
func TestSetSort_DifferentColumnRestartsAsc(t *testing.T) {
	v := sortableView(t, nil)
	v.SetSort(0)
	v.SetSort(0) // desc on col 0
	v.SetSort(1) // should be asc on col 1, not desc
	v.mu.RLock()
	defer v.mu.RUnlock()
	require.Equal(t, 1, v.sortState.col)
	require.Equal(t, SortAsc, v.sortState.dir)
}

// TestSetSort_OutOfRangeIsNoOp pins: SetSort on a col outside [0,
// len(cols)) is a no-op and does NOT alter the existing sort state.
func TestSetSort_OutOfRangeIsNoOp(t *testing.T) {
	v := sortableView(t, nil)
	v.SetSort(0)
	v.SetSort(99) // out of range
	v.SetSort(-1) // out of range
	v.mu.RLock()
	defer v.mu.RUnlock()
	require.Equal(t, 0, v.sortState.col, "out-of-range SetSort must not alter state")
	require.Equal(t, SortAsc, v.sortState.dir)
}

// TestSetSort_OnEmptyBufferDoesNotCrash pins: SetSort on a view with no
// rows is safe (it still sets sortState; the projection just yields an
// empty slice).
func TestSetSort_OnEmptyBufferDoesNotCrash(t *testing.T) {
	v := sortableView(t, nil)
	require.NotPanics(t, func() { v.SetSort(0) })
	require.True(t, v.SortActive())
	indices := projectIndices(v)
	require.Empty(t, indices)
}

// TestProject_SortIndicatorDoesNotReorder pins the dbsavvy-72k.6 contract:
// with a display-only sort indicator installed (via SetSortIndicator), the
// projection returns IDENTITY order — the grid no longer reorders rows
// (ordering is DB-side) — while the title still carries the " (sort: …)"
// suffix.
func TestProject_SortIndicatorDoesNotReorder(t *testing.T) {
	v := sortableView(t, [][]any{
		{"a", int64(10)},
		{"b", int64(9)},
		{"c", int64(2)},
	})
	v.SetTitle("query 1")
	v.SetSortIndicator(1, SortAsc) // indicate "age ↑" but do NOT reorder

	got := projectIndices(v)
	require.Equal(t, []int{0, 1, 2}, got,
		"projection must be identity order: the grid does not reorder rows")
	require.Equal(t, "query 1 (sort: age ↑)", v.Title(),
		"the display-only indicator must still appear in the title")
}

// TestSetColumns_ClearsSort pins AD-5: a fresh schema attach resets sort.
func TestSetColumns_ClearsSort(t *testing.T) {
	v := sortableView(t, [][]any{
		{"a", int64(1)},
	})
	v.SetSort(0)
	require.True(t, v.SortActive())
	v.SetColumns([]models.ColumnMeta{
		{Name: "x", TypeName: "text"},
	})
	require.False(t, v.SortActive(), "SetColumns must clear sort state")
}

// TestSortIndicator_TitleSuffix pins: when a sort is active Title()
// appends "(sort: <col_name> ↑/↓)". When inactive the indicator is "".
func TestSortIndicator_TitleSuffix(t *testing.T) {
	v := sortableView(t, nil)
	v.SetTitle("query 1")
	require.Equal(t, "query 1", v.Title(), "no sort indicator when inactive")

	v.SetSort(0) // asc on name
	require.Equal(t, "query 1 (sort: name ↑)", v.Title())

	v.SetSort(0) // desc
	require.Equal(t, "query 1 (sort: name ↓)", v.Title())
}

// TestSortIndicator_HandlesColumnRemoval pins: when the sort-column has
// been removed by a SetColumns the indicator collapses to "". (Defensive;
// SetColumns clears sort, so this is reached only via direct snapshot.)
func TestSortIndicator_HandlesColumnRemoval(t *testing.T) {
	s := sortState{col: 5, dir: SortAsc}
	require.Empty(t, sortIndicatorLocked(s, []models.ColumnMeta{{Name: "only"}}))
}
