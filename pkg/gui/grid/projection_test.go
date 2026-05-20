package grid

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// TestProjection_FilterThenSort pins AD-13 projection order: filter →
// sort → hide-cols. With a filter that drops some rows and a sort that
// reverses the rest, the rendered indices must contain only matching
// rows in sorted order.
func TestProjection_FilterThenSort(t *testing.T) {
	v := NewView()
	v.SetColumns([]models.ColumnMeta{
		{Name: "name", TypeName: "text", TypeOID: pgOIDText},
		{Name: "age", TypeName: "int4", TypeOID: pgOIDInt4},
	})
	// Mix of names; filter on "name" matching "a", then sort age asc.
	v.AppendRows([]models.Row{{Values: []any{"alice", int64(30)}}})  // 0 — match
	v.AppendRows([]models.Row{{Values: []any{"bob", int64(25)}}})    // 1 — no match
	v.AppendRows([]models.Row{{Values: []any{"alex", int64(28)}}})   // 2 — match
	v.AppendRows([]models.Row{{Values: []any{"charlie", int64(5)}}}) // 3 — no match
	v.AppendRows([]models.Row{{Values: []any{"alan", int64(40)}}})   // 4 — match

	require.NoError(t, v.SetFilter("^a", false))
	v.SetSort(1) // asc by age

	got := projectIndices(v)
	// Filter keeps {0, 2, 4}; sort asc by age: alex(28)→2, alice(30)→0, alan(40)→4.
	require.Equal(t, []int{2, 0, 4}, got)
}

// TestProjection_NonMatchingStayHiddenAfterSort pins: sort only reorders
// the filtered subset; rows excluded by the filter remain hidden.
func TestProjection_NonMatchingStayHiddenAfterSort(t *testing.T) {
	v := NewView()
	v.SetColumns([]models.ColumnMeta{
		{Name: "name", TypeName: "text", TypeOID: pgOIDText},
	})
	v.AppendRows([]models.Row{{Values: []any{"alpha"}}})
	v.AppendRows([]models.Row{{Values: []any{"bravo"}}})
	v.AppendRows([]models.Row{{Values: []any{"alfa"}}})

	require.NoError(t, v.SetFilter("^a", false))
	v.SetSort(0) // asc by name

	got := projectIndices(v)
	require.Equal(t, []int{2, 0}, got, "bravo (idx 1) must remain hidden after filter+sort")
}

// TestProjection_SortAloneNoFilter pins: with no filter, sort returns
// every row in sorted order.
func TestProjection_SortAloneNoFilter(t *testing.T) {
	v := sortableView(t, [][]any{
		{"a", int64(3)},
		{"b", int64(1)},
		{"c", int64(2)},
	})
	v.SetSort(1)
	got := projectIndices(v)
	require.Equal(t, []int{1, 2, 0}, got)
}

// TestHandleHeaderClick_DoubleClickFiresSort pins the AC: a left-click
// on column header followed by a second left-click on the same header
// within the configured window fires SetSort.
func TestHandleHeaderClick_DoubleClickFiresSort(t *testing.T) {
	v := sortableView(t, [][]any{
		{"x", int64(1)},
		{"y", int64(2)},
	})
	v.SetMouseDoubleClickMs(400)
	// Layout: col 0 width >= MinColumnWidth; widths get sized after
	// AppendRows. Click at x=2 (inside col 0). The exact x boundary
	// depends on widths but col 0 always starts at x=0.
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	v.HandleHeaderClick(2, 0, t0)
	require.False(t, v.SortActive(), "first click should only record, not sort")
	v.HandleHeaderClick(2, 0, t0.Add(100*time.Millisecond))
	require.True(t, v.SortActive(), "second click inside window should sort")
	v.mu.RLock()
	require.Equal(t, 0, v.sortState.col)
	require.Equal(t, SortAsc, v.sortState.dir)
	v.mu.RUnlock()
}

// TestHandleHeaderClick_OutsideWindowResets pins: when the second click
// arrives after the window expires, it does NOT fire a sort — it's
// recorded as a fresh first click instead.
func TestHandleHeaderClick_OutsideWindowResets(t *testing.T) {
	v := sortableView(t, [][]any{{"x", int64(1)}})
	v.SetMouseDoubleClickMs(400)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	v.HandleHeaderClick(2, 0, t0)
	v.HandleHeaderClick(2, 0, t0.Add(500*time.Millisecond))
	require.False(t, v.SortActive(), "second click outside window must not sort")
}

// TestHandleHeaderClick_DifferentColumnResetsPair pins: clicking a
// DIFFERENT column for the second click is NOT a double-click; the
// second click is treated as the start of a new pair.
func TestHandleHeaderClick_DifferentColumnResetsPair(t *testing.T) {
	v := sortableView(t, [][]any{{"x", int64(1)}})
	v.SetMouseDoubleClickMs(400)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	v.HandleHeaderClick(2, 0, t0)
	// Click on col 1: must be a new first-click, not a sort on col 0.
	col1X := effectiveWidth(v.snapshot().widths, 0) + 1 + 1 // start of col 1 + 1
	v.HandleHeaderClick(col1X, 0, t0.Add(50*time.Millisecond))
	require.False(t, v.SortActive(), "click on different column must not fire sort")
}

// TestHandleHeaderClick_TripleClickAscDesc pins the triple-click state
// machine: 1st = record; 2nd within window = SetSort(asc); 3rd within
// the window of the second = SetSort(desc).
func TestHandleHeaderClick_TripleClickAscDesc(t *testing.T) {
	v := sortableView(t, [][]any{{"x", int64(1)}})
	v.SetMouseDoubleClickMs(400)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	v.HandleHeaderClick(2, 0, t0)                           // record
	v.HandleHeaderClick(2, 0, t0.Add(100*time.Millisecond)) // asc
	v.HandleHeaderClick(2, 0, t0.Add(200*time.Millisecond)) // (3rd: new first)
	v.HandleHeaderClick(2, 0, t0.Add(300*time.Millisecond)) // desc
	v.mu.RLock()
	require.Equal(t, SortDesc, v.sortState.dir, "triple-click should land at desc")
	v.mu.RUnlock()
}

// TestHandleHeaderClick_NonHeaderRowIgnored pins: clicks on data rows
// (y > 0) are no-ops; sort state remains untouched.
func TestHandleHeaderClick_NonHeaderRowIgnored(t *testing.T) {
	v := sortableView(t, [][]any{{"x", int64(1)}})
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	v.HandleHeaderClick(2, 5, t0)
	v.HandleHeaderClick(2, 5, t0.Add(100*time.Millisecond))
	require.False(t, v.SortActive())
	// Ensure prior-click was NOT recorded either; a subsequent header
	// click pair must still need two clicks.
	v.HandleHeaderClick(2, 0, t0.Add(150*time.Millisecond))
	require.False(t, v.SortActive(), "single header click after data clicks should not sort")
}

// TestHandleHeaderClick_OutOfRangeX pins: clicking past the last visible
// column is a no-op (no recorded click, no sort).
func TestHandleHeaderClick_OutOfRangeX(t *testing.T) {
	v := sortableView(t, [][]any{{"x", int64(1)}})
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	v.HandleHeaderClick(9999, 0, t0)
	require.False(t, v.SortActive())
}
