package ui

import (
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// TestSortActiveTab_JoinResultIsSortable verifies the sortability guard passes
// for ANY result with >=1 grid column (joins/aggregates/CTEs included): the
// guard never consults DetectFromQuery. The mid-stream (StateRunning) tab is a
// valid sort target.
func TestSortActiveTab_JoinResultIsSortable(t *testing.T) {
	h, _, runner := newReRunHelper(t)
	tab := openRunningResultTab(t, h, runner)

	// A join-style origin: DetectFromQuery would reject it, but SortActiveTab
	// must not — sortability is grid-column-count only.
	joinSQL := "SELECT u.id, o.total FROM users u JOIN orders o ON o.user_id = u.id"
	tab.SetOrigin(joinSQL, nil, "")

	runSQL, run, toast := h.SortActiveTab(0)
	if !run {
		t.Fatalf("run = false, want true (join result must be sortable)")
	}
	if toast != "" {
		t.Errorf("toast = %q, want empty", toast)
	}
	want := wrapSorted(joinSQL, 1, sortAsc)
	if runSQL != want {
		t.Errorf("runSQL = %q, want %q", runSQL, want)
	}
}

// TestSortActiveTab_PendingEditsBlocks verifies the pending-edits guard:
// staged edits surface the toast and return run=false (no re-run).
func TestSortActiveTab_PendingEditsBlocks(t *testing.T) {
	h, _, runner := newReRunHelper(t)
	tab := openRunningResultTab(t, h, runner)

	pe := &models.PendingEditSet{}
	if err := pe.Add(models.PendingEdit{
		PrimaryKey: []any{1},
		Column:     "name",
		NewValue:   "edited",
		Kind:       models.Literal,
		LoadedAt:   time.Now(),
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	tab.Grid().SetPendingEdits(pe)

	runSQL, run, toast := h.SortActiveTab(0)
	if run {
		t.Fatalf("run = true, want false (pending edits must block sort)")
	}
	if toast != sortPendingEditsToast {
		t.Errorf("toast = %q, want %q", toast, sortPendingEditsToast)
	}
	if runSQL != "" {
		t.Errorf("runSQL = %q, want empty (no re-run on blocked sort)", runSQL)
	}
	// The tab's authoritative sort state must be untouched (cycle never ran).
	if tab.sortDir != sortClear {
		t.Errorf("sortDir = %v, want sortClear (blocked sort must not cycle)", tab.sortDir)
	}
}

// TestSortActiveTab_CycleAscDescClear verifies the asc→desc→clear cycle on one
// column, the 4th-call restart at asc, and that a different column restarts at
// asc.
func TestSortActiveTab_CycleAscDescClear(t *testing.T) {
	h, _, runner := newReRunHelper(t)
	tab := openRunningResultTab(t, h, runner)
	orig := reRunOrigSQL

	// 1st: asc.
	runSQL, run, _ := h.SortActiveTab(0)
	if !run || runSQL != wrapSorted(orig, 1, sortAsc) {
		t.Fatalf("call 1 = (%q, run=%v), want wrapped-asc", runSQL, run)
	}

	// 2nd: desc.
	runSQL, run, _ = h.SortActiveTab(0)
	if !run || runSQL != wrapSorted(orig, 1, sortDesc) {
		t.Fatalf("call 2 = (%q, run=%v), want wrapped-desc", runSQL, run)
	}

	// 3rd: clear → origSQL verbatim.
	runSQL, run, _ = h.SortActiveTab(0)
	if !run || runSQL != orig {
		t.Fatalf("call 3 = (%q, run=%v), want origSQL (clear)", runSQL, run)
	}

	// 4th: restart at asc.
	runSQL, run, _ = h.SortActiveTab(0)
	if !run || runSQL != wrapSorted(orig, 1, sortAsc) {
		t.Fatalf("call 4 = (%q, run=%v), want wrapped-asc (restart)", runSQL, run)
	}

	// Selecting a DIFFERENT column restarts at asc (ordinal = col+1 = 2).
	runSQL, run, _ = h.SortActiveTab(1)
	if !run || runSQL != wrapSorted(orig, 2, sortAsc) {
		t.Fatalf("different-column = (%q, run=%v), want wrapped-asc on col 2", runSQL, run)
	}
	if tab.sortCol != 1 || tab.sortDir != sortAsc {
		t.Errorf("after column switch (sortCol,sortDir) = (%d,%v), want (1,sortAsc)", tab.sortCol, tab.sortDir)
	}
}

// TestSortActiveTab_ReentrancyBlockedWhileSorting verifies that a sort request
// while a sort re-run is already in flight (StateSorting) is a silent no-op, so
// two triggers launch exactly one re-run. A mid-stream sort (StateRunning) is
// NOT blocked — covered by the other tests.
func TestSortActiveTab_ReentrancyBlockedWhileSorting(t *testing.T) {
	h, _, runner := newReRunHelper(t)
	tab := openRunningResultTab(t, h, runner)

	// First sort: allowed (mid-stream StateRunning), cycles to asc.
	if _, run, _ := h.SortActiveTab(0); !run {
		t.Fatalf("first sort run = false, want true")
	}

	// Simulate the re-run being in flight: ReattachActiveTab sets StateSorting.
	tab.mu.Lock()
	tab.state = StateSorting
	tab.mu.Unlock()

	runSQL, run, toast := h.SortActiveTab(0)
	if run {
		t.Fatalf("second sort run = true while StateSorting, want false (single re-run)")
	}
	if toast != "" {
		t.Errorf("toast = %q, want empty (silent re-entrancy no-op)", toast)
	}
	if runSQL != "" {
		t.Errorf("runSQL = %q, want empty", runSQL)
	}
	// The cycle must NOT have advanced (still asc from the first call).
	if tab.sortDir != sortAsc {
		t.Errorf("sortDir = %v, want sortAsc (re-entrant call must not cycle)", tab.sortDir)
	}
}

// TestSortActiveTab_NoActiveTabSilentNoOp verifies the silent no-op when no tab
// is active.
func TestSortActiveTab_NoActiveTabSilentNoOp(t *testing.T) {
	h, _, _ := newReRunHelper(t)
	runSQL, run, toast := h.SortActiveTab(0)
	if run || runSQL != "" || toast != "" {
		t.Fatalf("no active tab = (%q, run=%v, toast=%q), want silent no-op", runSQL, run, toast)
	}
}
