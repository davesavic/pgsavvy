package grid

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// TestProjection_FilterSubsetsPreservingOrder pins the projection: filter
// drops non-matching rows and the surviving matches keep their original
// (raw) order. The grid no longer reorders for sort (dbsavvy-72k.6), so
// even with a sort indicator installed the matches must stay in raw order.
func TestProjection_FilterSubsetsPreservingOrder(t *testing.T) {
	v := NewView()
	v.SetColumns([]models.ColumnMeta{
		{Name: "name", TypeName: "text"},
		{Name: "age", TypeName: "int4"},
	})
	// Mix of names; filter on "name" matching "^a".
	v.AppendRows([]models.Row{{Values: []any{"alice", int64(30)}}})  // 0 — match
	v.AppendRows([]models.Row{{Values: []any{"bob", int64(25)}}})    // 1 — no match
	v.AppendRows([]models.Row{{Values: []any{"alex", int64(28)}}})   // 2 — match
	v.AppendRows([]models.Row{{Values: []any{"charlie", int64(5)}}}) // 3 — no match
	v.AppendRows([]models.Row{{Values: []any{"alan", int64(40)}}})   // 4 — match

	require.NoError(t, v.SetFilter("^a", false))
	v.SetSortIndicator(1, SortAsc) // display-only; must not reorder

	got := projectIndices(v)
	// Filter keeps {0, 2, 4} in raw order; the indicator does NOT reorder.
	require.Equal(t, []int{0, 2, 4}, got)
}

// TestProjection_NonMatchingStayHidden pins: rows excluded by the filter
// remain hidden, and the survivors keep their raw order (no reorder).
func TestProjection_NonMatchingStayHidden(t *testing.T) {
	v := NewView()
	v.SetColumns([]models.ColumnMeta{
		{Name: "name", TypeName: "text"},
	})
	v.AppendRows([]models.Row{{Values: []any{"alpha"}}})
	v.AppendRows([]models.Row{{Values: []any{"bravo"}}})
	v.AppendRows([]models.Row{{Values: []any{"alfa"}}})

	require.NoError(t, v.SetFilter("^a", false))
	v.SetSortIndicator(0, SortAsc) // display-only; must not reorder

	got := projectIndices(v)
	require.Equal(t, []int{0, 2}, got, "bravo (idx 1) hidden; alpha/alfa keep raw order")
}

// TestProjection_SortAloneNoFilterIsIdentity pins: with no filter, the
// projection returns every row in raw order even when a sort indicator is
// set — ordering is DB-side, so the grid never reorders.
func TestProjection_SortAloneNoFilterIsIdentity(t *testing.T) {
	v := sortableView(t, [][]any{
		{"a", int64(3)},
		{"b", int64(1)},
		{"c", int64(2)},
	})
	v.SetSortIndicator(1, SortAsc)
	got := projectIndices(v)
	require.Equal(t, []int{0, 1, 2}, got, "no filter + indicator → identity order")
}

// captureSortRequests installs a capturing onSortRequest hook on v and
// returns a pointer to the slice of RAW column indices it was invoked with.
// The grid no longer owns the sort cycle (dbsavvy-72k.5), so the
// double-click DETECTION is now verified by observing the sink, not the
// grid's sortState.
func captureSortRequests(v *View) *[]int {
	got := &[]int{}
	v.SetOnSortRequest(func(col int) { *got = append(*got, col) })
	return got
}

// TestHandleHeaderClick_DoubleClickFiresSort pins the AC: a left-click
// on column header followed by a second left-click on the same header
// within the configured window fires onSortRequest with the RAW col —
// once, on the second click only.
func TestHandleHeaderClick_DoubleClickFiresSort(t *testing.T) {
	v := sortableView(t, [][]any{
		{"x", int64(1)},
		{"y", int64(2)},
	})
	v.SetMouseDoubleClickMs(400)
	got := captureSortRequests(v)
	// Layout: col 0 width >= MinColumnWidth; widths get sized after
	// AppendRows. Click at x=2 (inside col 0). The exact x boundary
	// depends on widths but col 0 always starts at x=0.
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	v.HandleHeaderClick(2, 0, t0)
	require.Empty(t, *got, "first click should only record, not fire onSortRequest")
	v.HandleHeaderClick(2, 0, t0.Add(100*time.Millisecond))
	require.Equal(t, []int{0}, *got, "second click inside window should fire onSortRequest with raw col 0")
	require.False(t, v.SortActive(), "the grid no longer owns the sort cycle")
}

// TestHandleHeaderClick_OutsideWindowResets pins: when the second click
// arrives after the window expires, it does NOT fire a sort — it's
// recorded as a fresh first click instead.
func TestHandleHeaderClick_OutsideWindowResets(t *testing.T) {
	v := sortableView(t, [][]any{{"x", int64(1)}})
	v.SetMouseDoubleClickMs(400)
	got := captureSortRequests(v)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	v.HandleHeaderClick(2, 0, t0)
	v.HandleHeaderClick(2, 0, t0.Add(500*time.Millisecond))
	require.Empty(t, *got, "second click outside window must not fire onSortRequest")
}

// TestHandleHeaderClick_DifferentColumnResetsPair pins: clicking a
// DIFFERENT column for the second click is NOT a double-click; the
// second click is treated as the start of a new pair.
func TestHandleHeaderClick_DifferentColumnResetsPair(t *testing.T) {
	v := sortableView(t, [][]any{{"x", int64(1)}})
	v.SetMouseDoubleClickMs(400)
	got := captureSortRequests(v)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	v.HandleHeaderClick(2, 0, t0)
	// Click on col 1: must be a new first-click, not a sort on col 0.
	col1X := effectiveWidth(v.snapshot().widths, 0) + 1 + 1 // start of col 1 + 1
	v.HandleHeaderClick(col1X, 0, t0.Add(50*time.Millisecond))
	require.Empty(t, *got, "click on different column must not fire onSortRequest")
}

// TestHandleHeaderClick_TripleClickAscDesc pins the qualifying-double-click
// machine: each Nth qualifying double-click fires onSortRequest once with
// the same RAW col. Direction (asc/desc) is no longer the grid's concern —
// the Tab-level flow owns the cycle (dbsavvy-72k.5) — so here we assert TWO
// qualifying double-clicks fire onSortRequest twice with col 0.
func TestHandleHeaderClick_TripleClickAscDesc(t *testing.T) {
	v := sortableView(t, [][]any{{"x", int64(1)}})
	v.SetMouseDoubleClickMs(400)
	got := captureSortRequests(v)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	v.HandleHeaderClick(2, 0, t0)                           // record
	v.HandleHeaderClick(2, 0, t0.Add(100*time.Millisecond)) // 1st double-click → fire
	v.HandleHeaderClick(2, 0, t0.Add(200*time.Millisecond)) // new first
	v.HandleHeaderClick(2, 0, t0.Add(300*time.Millisecond)) // 2nd double-click → fire
	require.Equal(t, []int{0, 0}, *got, "two qualifying double-clicks fire onSortRequest twice with the same col")
}

// TestHandleHeaderClick_NonHeaderRowIgnored pins: clicks on data rows
// (y > 0) are no-ops; no sort request fires.
func TestHandleHeaderClick_NonHeaderRowIgnored(t *testing.T) {
	v := sortableView(t, [][]any{{"x", int64(1)}})
	got := captureSortRequests(v)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	v.HandleHeaderClick(2, 5, t0)
	v.HandleHeaderClick(2, 5, t0.Add(100*time.Millisecond))
	require.Empty(t, *got)
	// Ensure prior-click was NOT recorded either; a subsequent header
	// click pair must still need two clicks.
	v.HandleHeaderClick(2, 0, t0.Add(150*time.Millisecond))
	require.Empty(t, *got, "single header click after data clicks should not fire onSortRequest")
}

// TestHandleHeaderClick_OutOfRangeX pins: clicking past the last visible
// column is a no-op (no recorded click, no sort request).
func TestHandleHeaderClick_OutOfRangeX(t *testing.T) {
	v := sortableView(t, [][]any{{"x", int64(1)}})
	got := captureSortRequests(v)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	v.HandleHeaderClick(9999, 0, t0)
	require.Empty(t, *got)
}
