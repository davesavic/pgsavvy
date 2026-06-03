package orchestrator_test

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/dbsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/query"
)

// fireHistoryOpen looks up the HistoryOpen command and invokes its
// handler with a zero ExecCtx. Fails the test on a missing command.
func fireHistoryOpen(t *testing.T, g *orchestrator.Gui) {
	t.Helper()
	cmd, ok := g.CommandRegistry().Get(commands.HistoryOpen)
	if !ok || cmd == nil || cmd.Handler == nil {
		t.Fatalf("HistoryOpen not registered or handler nil")
	}
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("HistoryOpen handler: %v", err)
	}
}

// fireHistoryConfirm invokes the trait <cr> confirm action registered for
// the HISTORY scope (list.confirm:HISTORY). Fails on a missing command.
func fireHistoryConfirm(t *testing.T, g *orchestrator.Gui) {
	t.Helper()
	id := commands.ListConfirm + ":" + string(types.HISTORY)
	cmd, ok := g.CommandRegistry().Get(id)
	if !ok || cmd == nil || cmd.Handler == nil {
		t.Fatalf("history confirm action %q not registered or handler nil", id)
	}
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("history confirm handler: %v", err)
	}
}

// buildTestGuiNoHistory mirrors buildTestGuiWithHistory but injects a
// HistoryProvider that returns (nil, nil) so g.history stays nil. Exercises
// the unopened-store path.
func buildTestGuiNoHistory(t *testing.T) *orchestrator.Gui {
	t.Helper()
	fs := afero.NewMemMapFs()
	log := slog.New(slog.DiscardHandler)
	cfg := config.GetDefaultConfig()
	c := common.NewCommon(log, i18n.EnglishTranslationSet(), cfg, &common.AppState{}, fs)
	store := common.NewAppStateStore(fs, "/tmp/state.yml", common.DefaultClock())

	tmp := t.TempDir()
	g := orchestrator.NewGui(orchestrator.Deps{
		Common:              c,
		Store:               store,
		ConnectionsPath:     filepath.Join(tmp, "connections.yml"),
		ConnectionsProvider: func() []models.Connection { return nil },
		DriverNamesFn:       func() []string { return []string{"postgres"} },
		HistoryProvider:     func() (*query.History, error) { return nil, nil },
	})
	rec := testfake.NewRecorderGuiDriver()
	if err := g.UseDriverForTest(rec); err != nil {
		t.Fatalf("UseDriverForTest: %v", err)
	}
	t.Cleanup(func() { _ = g.Close() })
	return g
}

func TestHistoryOpen_PushesPopup(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	fireHistoryOpen(t, g)
	g.WaitForWorkersForTest()

	top := g.ContextTree().Current()
	if top == nil {
		t.Fatal("focus stack top is nil after HistoryOpen")
	}
	if got := top.GetKey(); got != types.HISTORY {
		t.Fatalf("focus stack top = %q, want %q", got, types.HISTORY)
	}
}

func TestHistoryOpen_EmptyHistoryOpensWithAffordance(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	fireHistoryOpen(t, g)
	g.WaitForWorkersForTest()

	// Popup opened even though the store is empty.
	top := g.ContextTree().Current()
	if top == nil || top.GetKey() != types.HISTORY {
		t.Fatalf("HISTORY not on top after open on empty store: %v", top)
	}
	// Empty window selects nothing — the affordance is what renders.
	if _, ok := g.Registry().History.Selected(); ok {
		t.Fatal("Selected() returned ok on empty history; want no selection")
	}
	// HandleRender must not panic on the empty window.
	if err := g.Registry().History.HandleRender(); err != nil {
		t.Fatalf("HandleRender on empty history: %v", err)
	}
}

func TestHistoryOpen_NilHistoryIsNoOp(t *testing.T) {
	g := buildTestGuiNoHistory(t)

	beforeDepth := len(g.ContextTree().Stack())

	// Must not panic and must not push the popup.
	fireHistoryOpen(t, g)
	g.WaitForWorkersForTest()

	afterDepth := len(g.ContextTree().Stack())
	if afterDepth != beforeDepth {
		t.Fatalf("focus stack depth changed: before=%d after=%d (nil-history handler must be a no-op)", beforeDepth, afterDepth)
	}
	if top := g.ContextTree().Current(); top != nil && top.GetKey() == types.HISTORY {
		t.Fatal("HISTORY pushed despite nil history store")
	}
}

// waitForHistoryRecent polls the REAL store's Recent() read path (the same
// query the HistoryOpen worker runs) until at least wantRows are committed,
// giving the async batch writer up to 2s to flush. Mirrors the flush-wait
// pattern of query.waitForHistoryFlush but exercises the public Recent()
// surface so the external test package needs no DB handle. Bounded poll, not
// a fixed sleep: it returns the instant the writer drains its batch.
func waitForHistoryRecent(t *testing.T, h *query.History, wantRows int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := h.Recent(context.Background(), 500)
		if err == nil && len(rows) >= wantRows {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("history did not reach %d recent rows within 2s", wantRows)
}

// THE GAP closed here: queries RECORDED through the real *query.History write
// path must flow back through the HistoryOpen worker
// (OnWorker -> Recent -> OnUIThreadContentOnly -> SetRows) and appear in the
// popup newest-first. Unlike TestHistoryOpen_ConfirmInsertsSQLAndReturnsToEditor,
// this does NOT seed via direct SetRows — it proves the populated worker path.
func TestHistoryOpen_PopulatedWorkerPathLoadsRecordedRows(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	// Record through the real Record/async-writer path, then wait for the
	// batch writer to flush so the worker's single Recent() read sees them.
	store := g.HistoryStoreForTest()
	if store == nil {
		t.Fatal("HistoryStoreForTest() = nil; builder did not open the history store")
	}
	store.Record("SELECT 1 FROM alpha", 1, 1, true, "conn-a")
	store.Record("SELECT 2 FROM beta", 1, 1, true, "conn-a")
	store.Record("SELECT 3 FROM gamma", 1, 1, true, "conn-a")
	waitForHistoryRecent(t, store, 3)

	// Fire the handler: pushes HISTORY on the UI thread, loads Recent on a
	// worker, publishes rows via OnUIThreadContentOnly. Drain via the harness
	// worker-wait — no time.Sleep synchronization.
	fireHistoryOpen(t, g)
	g.WaitForWorkersForTest()

	// HISTORY is current/top.
	top := g.ContextTree().Current()
	if top == nil || top.GetKey() != types.HISTORY {
		t.Fatalf("HISTORY not on top after populated open: %v", top)
	}

	// The recorded rows flowed through the worker into the context, newest
	// first (most recent Record is first).
	items := g.Registry().History.Items()
	if len(items) != 3 {
		t.Fatalf("HISTORY rows = %d, want 3 (recorded rows must flow through the worker into the popup)", len(items))
	}
	got := make([]string, len(items))
	for i, it := range items {
		row, ok := it.(query.HistoryRow)
		if !ok {
			t.Fatalf("item %d is %T, want query.HistoryRow", i, it)
		}
		got[i] = row.SQL
	}
	want := []string{"SELECT 3 FROM gamma", "SELECT 2 FROM beta", "SELECT 1 FROM alpha"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("popup rows = %v, want newest-first %v", got, want)
		}
	}

	// Cursor selects the newest recorded row.
	sel, ok := g.Registry().History.Selected()
	if !ok {
		t.Fatal("Selected() returned no selection on populated history")
	}
	if sel.SQL != "SELECT 3 FROM gamma" {
		t.Fatalf("Selected().SQL = %q, want newest %q", sel.SQL, "SELECT 3 FROM gamma")
	}
}

func TestHistoryOpen_ConfirmInsertsSQLAndReturnsToEditor(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	fireHistoryOpen(t, g)
	g.WaitForWorkersForTest()

	depthAfterOpen := len(g.ContextTree().Stack())

	// Seed a selected row directly (the async writer makes store-seeding
	// racy in a unit test; SetRows on the UI thread is the deterministic
	// path and mirrors what the worker's OnUIThreadContentOnly does).
	const wantSQL = "SELECT 1 FROM users"
	g.Registry().History.SetRows([]query.HistoryRow{
		{ID: 1, SQL: wantSQL, Succeeded: true},
	})
	g.Registry().History.SetCursor(0)

	fireHistoryConfirm(t, g)
	g.WaitForWorkersForTest()

	// Confirm popped the popup back to the editor (Pop fires HandleFocus
	// on the new top — no explicit refocus needed).
	afterDepth := len(g.ContextTree().Stack())
	if afterDepth != depthAfterOpen-1 {
		t.Fatalf("focus stack depth after confirm = %d, want %d (popup should pop)", afterDepth, depthAfterOpen-1)
	}
	if top := g.ContextTree().Current(); top != nil && top.GetKey() == types.HISTORY {
		t.Fatal("HISTORY still on top after confirm; expected pop back to editor")
	}

	// The selected SQL was inserted into the query editor buffer.
	buf := g.Registry().QueryEditor.Buffer()
	if buf == nil {
		t.Fatal("QueryEditor buffer is nil")
	}
	if got := buf.String(); got != wantSQL {
		t.Fatalf("editor buffer = %q, want %q", got, wantSQL)
	}
}
