package ui

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/query"
)

// fakeColRowStream is a minimal drivers.RowStream double: it only reports a
// fixed column list (the helper's startStreaming reads Columns() before any
// row drain). Next is never called by the unit-test path (the fake
// StreamRunner drives appendRows directly), so it returns EOF.
type fakeColRowStream struct {
	cols []models.ColumnMeta
}

func (f *fakeColRowStream) Columns() []models.ColumnMeta { return f.cols }
func (f *fakeColRowStream) Next(context.Context) (models.Row, bool, error) {
	return models.Row{}, false, nil
}
func (f *fakeColRowStream) Close() error            { return nil }
func (f *fakeColRowStream) QueryID() models.QueryID { return models.QueryID{} }
func (f *fakeColRowStream) RowsAffected() int64     { return 0 }

// capturingStreamRunner records every NewQueryTask and captures the appendRows
// + onDone callbacks so a test can drive rows into the tab synchronously
// (the shared fakeStreamRunner discards both). It also counts Stop calls so a
// test can assert preemption stopped the prior stream.
type capturingStreamRunner struct {
	starts     int
	stops      int
	lastKey    string
	appendRows func([]models.Row)
	onDone     func(error)
}

func (c *capturingStreamRunner) NewQueryTask(
	taskKey string,
	_ func(ctx context.Context) (drivers.RowStream, error),
	appendRows func([]models.Row),
	_ int,
	onDone func(error),
) error {
	c.starts++
	c.lastKey = taskKey
	c.appendRows = appendRows
	c.onDone = onDone
	return nil
}

func (c *capturingStreamRunner) Stop()        { c.stops++ }
func (c *capturingStreamRunner) ReadRows(int) {}
func (c *capturingStreamRunner) ReadToEnd(fn func()) {
	if fn != nil {
		fn()
	}
}
func (c *capturingStreamRunner) EstimatedRows() int64   { return 0 }
func (c *capturingStreamRunner) SetEstimatedRows(int64) {}

// newReRunHelper builds a helper with an AppStateStore + a single shared
// capturing StreamRunner so a re-run reuses the same tab's runner.
func newReRunHelper(t *testing.T) (*ResultTabsHelper, *common.AppStateStore, *capturingStreamRunner) {
	t.Helper()
	store := common.NewAppStateStore(afero.NewMemMapFs(), "/tmp/state.yaml", common.DefaultClock())
	runner := &capturingStreamRunner{}
	h := NewResultTabsHelper(ResultTabsHelperDeps{
		Toast:         &fakeToaster{},
		Now:           time.Now,
		Store:         store,
		StreamFactory: func() StreamRunner { return runner },
	})
	return h, store, runner
}

const reRunOrigSQL = "SELECT * FROM public.users"

func reRunWrappedSQL() string { return wrapSorted(reRunOrigSQL, 1, sortAsc) }

// openRunningResultTab opens a streaming result tab seeded with two columns
// and one row, attaches the original identity + origin, and returns it. The
// tab is left in StateRunning (no onDone fired) so a re-run preempts a still-
// running stream.
func openRunningResultTab(t *testing.T, h *ResultTabsHelper, runner *capturingStreamRunner) *Tab {
	t.Helper()
	rh := newFakeRunHandle()
	rh.rows = &fakeColRowStream{cols: []models.ColumnMeta{{Name: "id"}, {Name: "name"}}}
	if err := h.openTab("Q", rh); err != nil {
		t.Fatalf("openTab: %v", err)
	}
	tab := h.Active()
	// Deliver an initial row so rowCount is non-zero pre-re-run.
	if runner.appendRows != nil {
		runner.appendRows([]models.Row{{Values: []any{1, "alice"}}})
	}
	tab.SetIdentity("connA", query.DetectFromQuery(reRunOrigSQL))
	tab.SetOrigin(reRunOrigSQL, nil, "")
	return tab
}

// TestReRun_WrappedReplacesRunningStreamNotNoOp verifies a re-run launches a
// NEW task into the SAME tab (same taskKey) rather than being deduped, and
// that the tab's row/state are reset for the fresh stream.
func TestReRun_WrappedReplacesRunningStreamNotNoOp(t *testing.T) {
	h, _, runner := newReRunHelper(t)
	tab := openRunningResultTab(t, h, runner)

	if got := tab.RowCount(); got != 1 {
		t.Fatalf("pre-re-run RowCount = %d, want 1", got)
	}
	startsBefore := runner.starts

	// Simulate the controller path: preempt the prior stream (RunQuery does
	// this via the preempt hook), then reattach the new RunHandle.
	h.PreemptInFlight()
	if runner.stops != 1 {
		t.Fatalf("PreemptInFlight Stop count = %d, want 1 (prior stream stopped)", runner.stops)
	}

	newRH := newFakeRunHandle()
	newRH.rows = &fakeColRowStream{cols: []models.ColumnMeta{{Name: "id"}, {Name: "name"}}}
	h.reattachActiveTab(newRH, reRunWrappedSQL(), reRunOrigSQL)

	// A new task was launched (NOT a no-op dedup) under the same taskKey.
	if runner.starts != startsBefore+1 {
		t.Fatalf("NewQueryTask starts = %d, want %d (re-run must launch a new task)", runner.starts, startsBefore+1)
	}
	wantKey := fmt.Sprintf("result_tab_%d", tab.ID())
	if runner.lastKey != wantKey {
		t.Errorf("taskKey = %q, want %q (same tab reused)", runner.lastKey, wantKey)
	}

	// Tab-level reset: rowCount cleared, not complete, no error, not cancelled.
	if got := tab.RowCount(); got != 0 {
		t.Errorf("post-re-run RowCount = %d, want 0", got)
	}
	if tab.Complete() {
		t.Errorf("post-re-run Complete = true, want false")
	}
	if tab.Err() != nil {
		t.Errorf("post-re-run Err = %v, want nil", tab.Err())
	}

	// New rows replace the old: the re-run stream delivers fresh rows.
	if runner.appendRows == nil {
		t.Fatal("appendRows not captured from re-run task")
	}
	runner.appendRows([]models.Row{{Values: []any{2, "bob"}}})
	if got := tab.RowCount(); got != 1 {
		t.Errorf("after first re-run row RowCount = %d, want 1", got)
	}
	if got := tab.Grid().RowCount(); got != 1 {
		t.Errorf("grid RowCount = %d, want 1 (grid cleared then re-filled)", got)
	}
}

// TestReRun_OriginIsWriteOnce verifies a wrapped re-run never overwrites the
// tab's origin: Origin() must still return the ORIGINAL statement so a later
// clear-sort re-runs the original (not a wrap-of-wrap). This pins the
// write-once invariant that the re-run path must honour.
func TestReRun_OriginIsWriteOnce(t *testing.T) {
	h, _, runner := newReRunHelper(t)
	tab := openRunningResultTab(t, h, runner)

	rh := newFakeRunHandle()
	rh.rows = &fakeColRowStream{cols: []models.ColumnMeta{{Name: "id"}, {Name: "name"}}}
	h.reattachActiveTab(rh, reRunWrappedSQL(), reRunOrigSQL)

	gotSQL, _, _ := tab.Origin()
	if gotSQL != reRunOrigSQL {
		t.Errorf("after wrapped re-run Origin() SQL = %q, want original %q (origin must be write-once)", gotSQL, reRunOrigSQL)
	}
}

// TestReRun_WrappedIsReadOnly_OriginalIsEditable verifies the gating identity
// recomputed from the SQL actually run: wrapped -> HasRowIdentity=false
// (read-only), original -> HasRowIdentity=true (editable).
func TestReRun_WrappedIsReadOnly_OriginalIsEditable(t *testing.T) {
	h, _, runner := newReRunHelper(t)
	tab := openRunningResultTab(t, h, runner)

	// Wrapped re-run: read-only identity.
	h.PreemptInFlight()
	wrapped := newFakeRunHandle()
	wrapped.rows = &fakeColRowStream{cols: []models.ColumnMeta{{Name: "id"}, {Name: "name"}}}
	h.reattachActiveTab(wrapped, reRunWrappedSQL(), reRunOrigSQL)

	if _, ri := tab.Identity(); ri.HasRowIdentity {
		t.Errorf("after wrapped re-run HasRowIdentity = true, want false (read-only)")
	}

	// Clear re-run (original SQL): editable identity restored.
	h.PreemptInFlight()
	cleared := newFakeRunHandle()
	cleared.rows = &fakeColRowStream{cols: []models.ColumnMeta{{Name: "id"}, {Name: "name"}}}
	h.reattachActiveTab(cleared, reRunOrigSQL, reRunOrigSQL)

	_, ri := tab.Identity()
	if !ri.HasRowIdentity {
		t.Errorf("after clear re-run HasRowIdentity = false, want true (editable)")
	}
	if ri.BaseTable != "public.users" {
		t.Errorf("after clear re-run BaseTable = %q, want public.users", ri.BaseTable)
	}
}

// TestReRun_SortingAffordanceUntilFirstRow verifies StateSorting shows until
// the first re-streamed row flips it to StateRunning, and that a zero-row
// re-run still reaches a terminal state.
func TestReRun_SortingAffordanceUntilFirstRow(t *testing.T) {
	h, _, runner := newReRunHelper(t)
	_ = openRunningResultTab(t, h, runner)

	h.PreemptInFlight()
	rh := newFakeRunHandle()
	rh.rows = &fakeColRowStream{cols: []models.ColumnMeta{{Name: "id"}, {Name: "name"}}}
	tab := h.Active()
	h.reattachActiveTab(rh, reRunWrappedSQL(), reRunOrigSQL)

	if got := tab.State(); got != StateSorting {
		t.Fatalf("state before first row = %q, want %q", got, StateSorting)
	}
	// Title renders the affordance.
	if got := tab.Title(); got != "~0 rows · sorting…" {
		t.Errorf("Title = %q, want %q", got, "~0 rows · sorting…")
	}

	runner.appendRows([]models.Row{{Values: []any{2, "bob"}}})
	if got := tab.State(); got != StateRunning {
		t.Errorf("state after first row = %q, want %q", got, StateRunning)
	}

	runner.onDone(nil)
	if got := tab.State(); got != StateComplete {
		t.Errorf("state after onDone = %q, want %q", got, StateComplete)
	}
}

// TestReRun_ZeroRowCompletesNotStuckSorting verifies a re-run that completes
// before any row arrives leaves StateSorting for a terminal state.
func TestReRun_ZeroRowCompletesNotStuckSorting(t *testing.T) {
	h, _, runner := newReRunHelper(t)
	_ = openRunningResultTab(t, h, runner)

	h.PreemptInFlight()
	rh := newFakeRunHandle()
	rh.rows = &fakeColRowStream{cols: []models.ColumnMeta{{Name: "id"}, {Name: "name"}}}
	tab := h.Active()
	h.reattachActiveTab(rh, reRunWrappedSQL(), reRunOrigSQL)

	runner.onDone(nil) // no rows delivered
	if got := tab.State(); got != StateComplete {
		t.Errorf("zero-row re-run state = %q, want %q (not stuck on sorting)", got, StateComplete)
	}
}

// TestReRun_CursorResetToTop verifies grid cursor/offset are reset to (0,0)
// after a re-run (id=1 at top).
func TestReRun_CursorResetToTop(t *testing.T) {
	h, _, runner := newReRunHelper(t)
	tab := openRunningResultTab(t, h, runner)

	// Fill the original stream with several rows and move the cursor down.
	g := tab.Grid()
	more := make([]models.Row, 10)
	for i := range more {
		more[i] = models.Row{Values: []any{i + 2, "x"}}
	}
	runner.appendRows(more)
	for range 5 {
		g.MoveCursorDown()
		g.Render(nil)
	}
	if row, _ := g.CursorPosition(); row == 0 {
		t.Fatalf("precondition: cursor still at row 0 after moves")
	}

	h.PreemptInFlight()
	rh := newFakeRunHandle()
	rh.rows = &fakeColRowStream{cols: []models.ColumnMeta{{Name: "id"}, {Name: "name"}}}
	h.reattachActiveTab(rh, reRunWrappedSQL(), reRunOrigSQL)

	row, col := tab.Grid().CursorPosition()
	if row != 0 || col != 0 {
		t.Errorf("post-re-run cursor = (%d,%d), want (0,0)", row, col)
	}
}

// TestReRun_HideColsReseededAgainstOriginalIdentity verifies the decoupling:
// the wrapped re-run attaches a read-only gating identity to the tab, but
// hide-cols are re-seeded against the ORIGINAL identity (which has a BaseTable
// matching the persisted set), so the user's hidden columns are restored even
// though the gating identity has no BaseTable.
func TestReRun_HideColsReseededAgainstOriginalIdentity(t *testing.T) {
	h, store, runner := newReRunHelper(t)
	tab := openRunningResultTab(t, h, runner)

	// Persist a hidden-col set under the ORIGINAL identity via the overlay.
	tab.Grid().SetColumns([]models.ColumnMeta{{Name: "id"}, {Name: "name"}})
	h.HideOverlay()
	h.HideOverlayMove(1) // cursor -> "name"
	h.HideOverlayToggle()
	h.HideOverlayClose()
	if got := store.HiddenColumnsSnapshot("connA", "public.users"); len(got) != 1 || got[0] != "name" {
		t.Fatalf("precondition: persisted hidden = %v, want [name]", got)
	}

	// Wrapped re-run: gating identity is read-only (no BaseTable), but the
	// helper re-seeds hide-cols against DetectFromQuery(origSQL).
	h.PreemptInFlight()
	rh := newFakeRunHandle()
	rh.rows = &fakeColRowStream{cols: []models.ColumnMeta{{Name: "id"}, {Name: "name"}}}
	h.reattachActiveTab(rh, reRunWrappedSQL(), reRunOrigSQL)

	// Gating identity is read-only ...
	if _, ri := tab.Identity(); ri.HasRowIdentity {
		t.Errorf("gating identity HasRowIdentity = true, want false")
	}
	// ... but hide-cols ("name" = index 1) were restored.
	hidden := tab.Grid().HiddenCols()
	if !hidden[1] || hidden[0] {
		t.Errorf("restored HiddenCols = %v, want only index 1 (name) hidden", hidden)
	}
}
