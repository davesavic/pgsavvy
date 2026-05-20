package grid

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// sortableView builds a view with two columns (name:text, age:int4) and
// the supplied rows installed. Used by the sort-comparator tests.
func sortableView(t *testing.T, rows [][]any) *View {
	t.Helper()
	v := NewView()
	v.SetColumns([]models.ColumnMeta{
		{Name: "name", TypeName: "text", TypeOID: pgOIDText},
		{Name: "age", TypeName: "int4", TypeOID: pgOIDInt4},
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

// TestApplySort_IntAscending pins the int-OID comparator: column with
// values [10, 9, 2] sorts as [2, 9, 10] (in raw-row-index space:
// indices [2, 1, 0]).
func TestApplySort_IntAscending(t *testing.T) {
	v := sortableView(t, [][]any{
		{"a", int64(10)},
		{"b", int64(9)},
		{"c", int64(2)},
	})
	v.SetSort(1) // asc on age
	got := projectIndices(v)
	require.Equal(t, []int{2, 1, 0}, got)
}

// TestApplySort_IntDescending pins desc direction on the same int data.
func TestApplySort_IntDescending(t *testing.T) {
	v := sortableView(t, [][]any{
		{"a", int64(10)},
		{"b", int64(9)},
		{"c", int64(2)},
	})
	v.SetSort(1) // asc
	v.SetSort(1) // desc
	got := projectIndices(v)
	require.Equal(t, []int{0, 1, 2}, got)
}

// TestApplySort_UnknownOIDFallsBackToString pins the AC edge case:
// "column with values [10, 9, 2] sorts as [10, 2, 9] for unknown OID
// (string)". The string compare puts "10" before "2" because '1' < '2'.
func TestApplySort_UnknownOIDFallsBackToString(t *testing.T) {
	v := NewView()
	v.SetColumns([]models.ColumnMeta{
		{Name: "val", TypeName: "?", TypeOID: 0},
	})
	v.AppendRows([]models.Row{{Values: []any{int64(10)}}})
	v.AppendRows([]models.Row{{Values: []any{int64(9)}}})
	v.AppendRows([]models.Row{{Values: []any{int64(2)}}})
	v.SetSort(0)
	got := projectIndices(v)
	require.Equal(t, []int{0, 2, 1}, got, "unknown-OID sort = string order: 10, 2, 9")
}

// TestApplySort_TimeComparator pins the timestamp comparator path.
func TestApplySort_TimeComparator(t *testing.T) {
	t0 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	v := NewView()
	v.SetColumns([]models.ColumnMeta{
		{Name: "at", TypeName: "timestamp", TypeOID: pgOIDTimestamp},
	})
	v.AppendRows([]models.Row{{Values: []any{t1}}})
	v.AppendRows([]models.Row{{Values: []any{t2}}})
	v.AppendRows([]models.Row{{Values: []any{t0}}})
	v.SetSort(0)
	got := projectIndices(v)
	require.Equal(t, []int{2, 0, 1}, got)
}

// TestApplySort_FloatComparator pins numeric values arriving as strings
// (pg numerics) parse and sort numerically, not lexically.
func TestApplySort_FloatComparator(t *testing.T) {
	v := NewView()
	v.SetColumns([]models.ColumnMeta{
		{Name: "n", TypeName: "numeric", TypeOID: pgOIDNumeric},
	})
	v.AppendRows([]models.Row{{Values: []any{"10.5"}}})
	v.AppendRows([]models.Row{{Values: []any{"2.0"}}})
	v.AppendRows([]models.Row{{Values: []any{"9.99"}}})
	v.SetSort(0)
	got := projectIndices(v)
	// numeric order: 2.0 < 9.99 < 10.5 → indices 1, 2, 0
	require.Equal(t, []int{1, 2, 0}, got)
}

// TestApplySort_NullValuesGoLast pins: NULLs sort to the bottom in asc
// order (matching PostgreSQL's default NULLS LAST semantics) and stay
// stable among themselves.
func TestApplySort_NullValuesGoLast(t *testing.T) {
	v := sortableView(t, [][]any{
		{"a", int64(5)},
		{"b", nil},
		{"c", int64(1)},
		{"d", nil},
	})
	v.SetSort(1)
	got := projectIndices(v)
	// non-null asc: idx 2 (1), idx 0 (5), then nulls in original order: idx 1, idx 3
	require.Equal(t, []int{2, 0, 1, 3}, got)
}

// TestApplySort_StableForEqualKeys pins the AC: "sort is stable (rows
// with equal keys retain insertion order)."
func TestApplySort_StableForEqualKeys(t *testing.T) {
	v := sortableView(t, [][]any{
		{"first", int64(5)},
		{"second", int64(5)},
		{"third", int64(5)},
		{"alpha", int64(1)},
	})
	v.SetSort(1)
	got := projectIndices(v)
	// asc by age: idx 3 (age=1), then 0, 1, 2 in original order.
	require.Equal(t, []int{3, 0, 1, 2}, got)
}

// TestSetColumns_ClearsSort pins AD-5: a fresh schema attach resets sort.
func TestSetColumns_ClearsSort(t *testing.T) {
	v := sortableView(t, [][]any{
		{"a", int64(1)},
	})
	v.SetSort(0)
	require.True(t, v.SortActive())
	v.SetColumns([]models.ColumnMeta{
		{Name: "x", TypeName: "text", TypeOID: pgOIDText},
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
