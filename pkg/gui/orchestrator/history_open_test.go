package orchestrator_test

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/pgsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/query"
)

// fireHistoryOpen looks up the HistoryOpen command and invokes its handler
// with a zero ExecCtx. Under the tab model the handler switches the
// QUERY_RAIL active tab to History (SetActiveTab) — it does NOT push onto the
// focus stack. Fails the test on a missing command.
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

// TestHistoryOpen_SwitchesTabWithoutPushingPopup proves the tab migration:
// <leader>h (HistoryOpen) switches the QUERY_RAIL active tab to History WITHOUT
// pushing onto the focus stack. The container's ActiveLeafKey() becomes
// HISTORY and the focus-stack depth is unchanged (no popup pushed/popped).
func TestHistoryOpen_SwitchesTabWithoutPushingPopup(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	// Make QUERY_RAIL the current MAIN_CONTEXT so the depth comparison
	// reflects only what the handler does (the handler itself never touches
	// the stack — it calls the container's SetActiveTab).
	if err := g.ContextTree().Push(g.Registry().QueryRail); err != nil {
		t.Fatalf("Push(QueryRail): %v", err)
	}
	beforeDepth := len(g.ContextTree().Stack())

	fireHistoryOpen(t, g)
	g.WaitForWorkersForTest()

	// The active leaf is HISTORY: the tab switched.
	if got := g.Registry().QueryRail.ActiveLeafKey(); got != types.HISTORY {
		t.Fatalf("ActiveLeafKey() = %q after HistoryOpen, want %q", got, types.HISTORY)
	}
	// No popup pushed: the focus-stack depth is unchanged and QUERY_RAIL is
	// still the top.
	if afterDepth := len(g.ContextTree().Stack()); afterDepth != beforeDepth {
		t.Fatalf("focus stack depth changed: before=%d after=%d (tab switch must not push a popup)", beforeDepth, afterDepth)
	}
	if top := g.ContextTree().Current(); top == nil || top.GetKey() != types.QUERY_RAIL {
		t.Fatalf("focus stack top = %v, want QUERY_RAIL still on top after tab switch", top)
	}
}

// TestHistoryOpen_EmptyHistorySwitchesWithAffordance switches to the History
// tab on an empty store and proves the empty-window affordance renders without
// a selection and without panic — still under the no-push tab model.
func TestHistoryOpen_EmptyHistorySwitchesWithAffordance(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	fireHistoryOpen(t, g)
	g.WaitForWorkersForTest()

	// Tab switched even though the store is empty.
	if got := g.Registry().QueryRail.ActiveLeafKey(); got != types.HISTORY {
		t.Fatalf("ActiveLeafKey() = %q on empty store, want %q", got, types.HISTORY)
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

// TestHistoryOpen_NilHistoryIsNoOp confirms HistoryOpen never pushes a popup
// and never panics when the history store failed to open (nil). The handler
// still switches the active tab (the tab switch is store-independent), but the
// focus stack is untouched.
func TestHistoryOpen_NilHistoryIsNoOp(t *testing.T) {
	g := buildTestGuiNoHistory(t)

	beforeDepth := len(g.ContextTree().Stack())

	// Must not panic and must not push a popup.
	fireHistoryOpen(t, g)
	g.WaitForWorkersForTest()

	afterDepth := len(g.ContextTree().Stack())
	if afterDepth != beforeDepth {
		t.Fatalf("focus stack depth changed: before=%d after=%d (nil-history handler must not push)", beforeDepth, afterDepth)
	}
	if top := g.ContextTree().Current(); top != nil && top.GetKey() == types.HISTORY {
		t.Fatal("HISTORY pushed onto focus stack despite nil history store")
	}
}

// waitForHistoryRecent polls the REAL store's Recent() read path (the same
// query the History leaf reload hook runs) until at least wantRows are
// committed, giving the async batch writer up to 2s to flush. Bounded poll,
// not a fixed sleep: it returns the instant the writer drains its batch.
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

// TestHistoryOpen_ActivatingTabReloadsRecordedRows restores the worker-path
// integration coverage at the orchestrator level under the tab model. The row
// load relocated from the popup-open handler to the History leaf's SetReload
// hook (fires on tab activation via HandleFocus when stale). This test records
// through the REAL *query.History write path, then activates the History tab
// and asserts the leaf reload surfaces the recorded rows newest-first via
// OnWorker -> Recent -> OnUIThreadContentOnly -> RefreshRows. It does NOT seed
// via direct SetRows — it proves the populated worker path. (Closes the
// T5-review coverage-gap note.)
func TestHistoryOpen_ActivatingTabReloadsRecordedRows(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	// Record through the real Record/async-writer path, then wait for the
	// batch writer to flush so the leaf reload's single Recent() read sees them.
	store := g.HistoryStoreForTest()
	if store == nil {
		t.Fatal("HistoryStoreForTest() = nil; builder did not open the history store")
	}
	store.Record("SELECT 1 FROM alpha", 1, 1, true, "conn-a")
	store.Record("SELECT 2 FROM beta", 1, 1, true, "conn-a")
	store.Record("SELECT 3 FROM gamma", 1, 1, true, "conn-a")
	waitForHistoryRecent(t, store, 3)

	// Activate the History tab: SetActiveTab fires the leaf's HandleFocus,
	// which (stale on first activation) runs the reload hook — Recent on a
	// worker, rows published via OnUIThreadContentOnly. Drain via the harness
	// worker-wait — no time.Sleep synchronization.
	fireHistoryOpen(t, g)
	g.WaitForWorkersForTest()

	// HISTORY is the active leaf.
	if got := g.Registry().QueryRail.ActiveLeafKey(); got != types.HISTORY {
		t.Fatalf("ActiveLeafKey() = %q after activating History tab, want %q", got, types.HISTORY)
	}

	// The recorded rows flowed through the reload worker into the leaf, newest
	// first (most recent Record is first).
	items := g.Registry().History.Items()
	if len(items) != 3 {
		t.Fatalf("HISTORY rows = %d, want 3 (recorded rows must flow through the leaf reload hook)", len(items))
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
			t.Fatalf("history rows = %v, want newest-first %v", got, want)
		}
	}

	// Cursor selects the newest recorded row (RefreshRows preserves the
	// initial cursor at 0, which is the newest row).
	sel, ok := g.Registry().History.Selected()
	if !ok {
		t.Fatal("Selected() returned no selection on populated history")
	}
	if sel.SQL != "SELECT 3 FROM gamma" {
		t.Fatalf("Selected().SQL = %q, want newest %q", sel.SQL, "SELECT 3 FROM gamma")
	}
}

// TestHistoryOpen_ConfirmInsertsSQLAndSwitchesToEditor rewrites the old
// "confirm pops back to the editor" test for the tab model: <cr> on a history
// row inserts the selected SQL at the editor cursor (unrun) and switches the
// active tab back to the editor. There is NO pop — the focus stack depth is
// unchanged and QUERY_RAIL stays the top. Re-activating the editor tab runs
// the editor leaf's HandleFocus (mode -> Normal), which is the orchestrator-
// observable proof the switch fired (single-fire is asserted at the controller
// layer in history_controller_test.go).
func TestHistoryOpen_ConfirmInsertsSQLAndSwitchesToEditor(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	if err := g.ContextTree().Push(g.Registry().QueryRail); err != nil {
		t.Fatalf("Push(QueryRail): %v", err)
	}

	fireHistoryOpen(t, g)
	g.WaitForWorkersForTest()
	depthAfterOpen := len(g.ContextTree().Stack())

	// Seed a selected row directly (the async writer makes store-seeding racy
	// in a unit test; SetRows is the deterministic path and mirrors what the
	// reload's OnUIThreadContentOnly does).
	const wantSQL = "SELECT 1 FROM users"
	g.Registry().History.SetRows([]query.HistoryRow{
		{ID: 1, SQL: wantSQL, Succeeded: true},
	})
	g.Registry().History.SetCursor(0)

	fireHistoryConfirm(t, g)
	g.WaitForWorkersForTest()

	// No pop: the focus stack depth is unchanged and QUERY_RAIL stays top.
	if afterDepth := len(g.ContextTree().Stack()); afterDepth != depthAfterOpen {
		t.Fatalf("focus stack depth after confirm = %d, want %d (confirm must switch tabs, not pop)", afterDepth, depthAfterOpen)
	}
	if top := g.ContextTree().Current(); top == nil || top.GetKey() != types.QUERY_RAIL {
		t.Fatalf("focus stack top = %v, want QUERY_RAIL still on top after confirm", top)
	}

	// The active tab switched back to the editor leaf.
	if got := g.Registry().QueryRail.ActiveLeafKey(); got != types.QUERY_EDITOR {
		t.Fatalf("ActiveLeafKey() = %q after confirm, want %q (confirm switches to editor tab)", got, types.QUERY_EDITOR)
	}
	// Re-activating the editor leaf ran its HandleFocus, which sets the editor
	// mode to Normal — orchestrator-observable proof the switch fired.
	if mode := g.ModeStore().Get(types.QUERY_EDITOR); mode != types.ModeNormal {
		t.Fatalf("editor mode after confirm = %v, want ModeNormal (editor HandleFocus must have fired)", mode)
	}

	// The selected SQL was inserted at the (empty) editor cursor.
	buf := g.Registry().QueryEditor.Buffer()
	if buf == nil {
		t.Fatal("QueryEditor buffer is nil")
	}
	if got := buf.String(); got != wantSQL {
		t.Fatalf("editor buffer = %q, want %q", got, wantSQL)
	}
}

// TestQueryRailSwitchAwayMidInsert_ResetsModeAndPersistsDirtyBuffer covers the
// switch-away-mid-Insert regression: with the editor tab in Insert mode and a
// DIRTY buffer, switching to a list tab must (a) reset the editor's mode to
// Normal via the editor leaf's HandleFocusLost, and (b) persist the dirty
// buffer through the editor's SaveBuffer path (Dirty cleared on the happy
// path). NOTE: the clear-on-save-failure remark (toast + clear) is DEFERRED
// (pgsavvy-z4a3) and unsatisfiable today, so it is intentionally not tested
// here — only the happy path is asserted.
func TestQueryRailSwitchAwayMidInsert_ResetsModeAndPersistsDirtyBuffer(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	rail := g.Registry().QueryRail
	// Start on the editor tab.
	rail.SetActiveTab(controllers.QueryRailEditorTab)

	// Put the editor into Insert mode with a dirty, saveable buffer.
	g.ModeStore().Set(types.QUERY_EDITOR, types.ModeInsert)
	buf := g.Registry().QueryEditor.Buffer()
	if buf == nil {
		t.Fatal("QueryEditor buffer is nil")
	}
	buf.ConnectionID = "conn-1"
	buf.UUID = "deadbeef-1234-4567-89ab-cdef01234567"
	buf.Dirty = true

	// Switch away to a list tab. The container fires the editor leaf's
	// HandleFocusLost (mode reset + saveBufferIfDirty).
	rail.SetActiveTab(controllers.QueryRailHistoryTab)
	g.WaitForWorkersForTest()

	// (a) Mode reset: Get returns ModeNormal after the leaf's modes.Reset.
	if mode := g.ModeStore().Get(types.QUERY_EDITOR); mode != types.ModeNormal {
		t.Fatalf("editor mode after switch-away = %v, want ModeNormal (HandleFocusLost must reset mode)", mode)
	}
	// (b) Dirty buffer persisted: the save path ran and cleared Dirty.
	if buf.Dirty {
		t.Fatal("buffer.Dirty == true after switch-away; want false (SaveBuffer must have flushed the dirty buffer)")
	}
}

// TestQueryRailContainerFlushOnModalPush covers the D3 container-flush path:
// with a DIRTY editor buffer and a LIST tab active, pushing a MAIN_CONTEXT
// (CONNECTION_MANAGER) triggers removeMain -> QUERY_RAIL.HandleFocusLost ->
// flushEditorLeaf, which flushes the editor leaf's dirty buffer even though a
// list tab is active. Without the unconditional flush, edits typed in the
// editor then left via a list tab would be silently dropped.
func TestQueryRailContainerFlushOnModalPush(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	rail := g.Registry().QueryRail
	// Make QUERY_RAIL the current MAIN_CONTEXT so a subsequent MAIN push
	// displaces it via removeMain.
	if err := g.ContextTree().Push(rail); err != nil {
		t.Fatalf("Push(QueryRail): %v", err)
	}

	// Dirty the editor buffer, then move to a LIST tab so the editor leaf is
	// NOT the active leaf — the flush must still reach it.
	buf := g.Registry().QueryEditor.Buffer()
	if buf == nil {
		t.Fatal("QueryEditor buffer is nil")
	}
	buf.ConnectionID = "conn-1"
	buf.UUID = "deadbeef-1234-4567-89ab-cdef01234567"
	buf.Dirty = true
	rail.SetActiveTab(controllers.QueryRailHistoryTab)
	if got := rail.ActiveLeafKey(); got != types.HISTORY {
		t.Fatalf("ActiveLeafKey() = %q, want HISTORY (list tab must be active for the D3 path)", got)
	}

	// Open the CONNECTION_MANAGER (a MAIN_CONTEXT). The push runs removeMain,
	// which fires QUERY_RAIL.HandleFocusLost -> flushEditorLeaf.
	if err := g.ContextTree().Push(g.Registry().ConnectionManager); err != nil {
		t.Fatalf("Push(ConnectionManager): %v", err)
	}
	g.WaitForWorkersForTest()

	// The editor's dirty buffer was flushed despite a list tab being active.
	if buf.Dirty {
		t.Fatal("buffer.Dirty == true after CONNECTION_MANAGER push; want false (container flush must reach the editor leaf)")
	}
}
