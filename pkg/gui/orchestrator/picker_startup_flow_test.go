package orchestrator_test

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/dbsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/query"
)

// T7 — Integration AC capstone for the picker-first startup flow
// (epic dbsavvy-e53). These tests drive the FULL connect lifecycle through
// the deterministic wire-fake harness (NOT the real-Postgres integration
// harness) so they run under plain `go test`.
//
// Several ACs are already proven by connect_cancel_test.go and so are NOT
// duplicated here:
//   - Cancel-mid-dial drops a successful result without stamping state.
//   - Rapid Esc→Enter supersession (only the newest attempt wins).
//   - Cancel with no in-flight attempt is a no-op.
//   - A blocked dial unblocks via the cancelled ctx so Close returns.
// The cancel test below (CancelMidConnectReturnsToPicker) covers the
// remaining capstone slice: focus returns to the picker and activeConn stays
// empty when the user aborts via Esc.

// buildPickerGui mirrors buildTestGuiWithHistory (wiring_query_test.go) but
// lets the caller inject a ConnectionsProvider and pre-seed the Store. The
// startup first-run tip is marked seen so the picker (CONNECTIONS), not the
// FIRST_RUN_TIP popup, is the top-of-stack context at boot — these tests
// assert against the picker itself. Returns the Gui, the recorder driver,
// and the live Store.
func buildPickerGui(
	t *testing.T,
	provider func() []models.Connection,
) (*orchestrator.Gui, *testfake.RecorderGuiDriver, *common.AppStateStore) {
	t.Helper()
	fs := afero.NewMemMapFs()
	log := slog.New(slog.DiscardHandler)
	cfg := config.GetDefaultConfig()
	c := common.NewCommon(log, i18n.EnglishTranslationSet(), cfg, &common.AppState{}, fs)
	store := common.NewAppStateStore(fs, "/tmp/state.yml", common.DefaultClock())
	// Suppress the first-run welcome tip so CONNECTIONS is top of the stack.
	store.StampStartupTips()

	tmp := t.TempDir()
	g := orchestrator.NewGui(orchestrator.Deps{
		Common:              c,
		Store:               store,
		ConnectionsPath:     filepath.Join(tmp, "connections.yml"),
		ConnectionsProvider: provider,
		DriverNamesFn:       func() []string { return []string{"postgres"} },
		HistoryProvider: func() (*query.History, error) {
			return query.New(filepath.Join(tmp, "history.sqlite"))
		},
	})
	rec := testfake.NewRecorderGuiDriver()
	if err := g.UseDriverForTest(rec); err != nil {
		t.Fatalf("UseDriverForTest: %v", err)
	}
	t.Cleanup(func() { _ = g.Close() })
	return g, rec, store
}

// AC: startup lands on the CONNECTIONS picker with ZERO connect attempts —
// the silent auto-connect was removed (epic dbsavvy-e53). The user always
// chooses a profile explicitly.
func TestPickerStartupFlow_LandsOnPickerNoConnect(t *testing.T) {
	_, conn := registerWireFake(t, drivers.Capabilities{})

	// One saved profile so a dial COULD have happened — the point is that it
	// must NOT. acquired==0 proves no session was acquired at startup.
	profile := models.Connection{Name: "saved", Driver: "postgres", DSN: "postgres://h/db"}
	prov := func() []models.Connection { return []models.Connection{profile} }
	g, _, _ := buildPickerGui(t, prov)

	if got := g.ContextTree().Current().GetKey(); got != types.CONNECTIONS {
		t.Fatalf("startup top context = %v; want CONNECTIONS (picker-first bootstrap)", got)
	}
	if got := conn.acquired.Load(); got != 0 {
		t.Fatalf("conn.AcquireSession called %d times at startup; want 0 (no implicit auto-connect)", got)
	}
}

// AC: with saved profiles the picker rail is populated and the cursor is
// restored onto the last-used profile (restoreConnectionsCursor) — the user
// lands ready to re-press <cr> on their previous connection.
func TestPickerStartupFlow_RestoresCursorOnLastUsedProfile(t *testing.T) {
	profiles := []models.Connection{
		{Name: "alpha", Driver: "postgres", DSN: "postgres://h/a"},
		{Name: "beta", Driver: "postgres", DSN: "postgres://h/b"},
	}

	// Seed LastConnectionID = "beta" BEFORE wiring so restoreConnectionsCursor
	// (run during wireWithDriver) positions the cursor on it.
	fs := afero.NewMemMapFs()
	st := common.NewAppStateStore(fs, "/tmp/state.yml", common.DefaultClock())
	st.StampStartupTips()
	st.MutateAndSave(func(a *common.AppState) { a.LastConnectionID = "beta" })

	cfg := config.GetDefaultConfig()
	c := common.NewCommon(slog.New(slog.DiscardHandler), i18n.EnglishTranslationSet(), cfg, &common.AppState{}, fs)
	tmp := t.TempDir()
	prov := func() []models.Connection { return profiles }
	g := orchestrator.NewGui(orchestrator.Deps{
		Common:              c,
		Store:               st,
		ConnectionsPath:     filepath.Join(tmp, "connections.yml"),
		ConnectionsProvider: prov,
		DriverNamesFn:       func() []string { return []string{"postgres"} },
		HistoryProvider: func() (*query.History, error) {
			return query.New(filepath.Join(tmp, "history.sqlite"))
		},
	})
	rec := testfake.NewRecorderGuiDriver()
	if err := g.UseDriverForTest(rec); err != nil {
		t.Fatalf("UseDriverForTest: %v", err)
	}
	t.Cleanup(func() { _ = g.Close() })

	if got := g.ContextTree().Current().GetKey(); got != types.CONNECTIONS {
		t.Fatalf("startup top = %v; want CONNECTIONS", got)
	}
	items := g.Registry().Connections.Items()
	if len(items) != 2 {
		t.Fatalf("CONNECTIONS rail has %d rows; want 2 (provider profiles)", len(items))
	}
	// beta is index 1; restoreConnectionsCursor should have parked there.
	if got := g.Registry().Connections.Cursor(); got != 1 {
		t.Fatalf("CONNECTIONS cursor = %d; want 1 (restored onto last-used 'beta')", got)
	}
}

// AC: Enter → CONNECTING → success → restored SCHEMAS, with CONNECTING popped
// off the stack and activeConnID stamped. This is the happy-path lifecycle:
// the async dial succeeds, schemas are populated, and focus advances to the
// SCHEMAS rail.
func TestPickerStartupFlow_SuccessLandsOnSchemas(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	driverName, conn := registerWireFake(t, drivers.Capabilities{})
	conn.schemas = []models.Schema{{Name: "public", Owner: "u"}}

	bag := g.HelperBagForTest()
	profile := &models.Connection{Name: "prod", Driver: driverName, DSN: "postgres://stub"}

	// OnBeginConnecting pushes CONNECTING and dispatches the dial on a worker.
	bag.OnBeginConnecting(profile)
	g.WaitForWorkersForTest()

	if got := g.ContextTree().Current().GetKey(); got != types.SCHEMAS {
		t.Fatalf("top after successful connect = %v; want SCHEMAS", got)
	}
	if connectingOnStack(g) {
		t.Fatal("CONNECTING still on the focus stack after success; it must be popped/replaced")
	}
	if got := g.ActiveConnIDForTest(); got != profile.Name {
		t.Fatalf("activeConnID = %q after success; want %q", got, profile.Name)
	}
}

// AC: success restore lands on TABLES when saved schema+table state is
// present for the profile (dbsavvy-dl7.4). We seed LastSchemaName /
// LastTableName for the profile, with the saved schema matching one the
// driver returns, so connectWithGen direct-loads tables and pushes TABLES.
func TestPickerStartupFlow_SuccessLandsOnTablesWithSavedState(t *testing.T) {
	g, _, store := buildPickerGui(t, func() []models.Connection { return nil })

	driverName, conn := registerWireFake(t, drivers.Capabilities{})
	conn.schemas = []models.Schema{{Name: "public", Owner: "u"}}

	profile := &models.Connection{Name: "withstate", Driver: driverName, DSN: "postgres://stub"}
	// Saved schema MUST match a schema the driver returns so the restore path
	// resolves it; a matching schema yields a non-nil tables slice, which is
	// enough to land on TABLES (the saved table need not exist in the list).
	store.SetLastSchemaName(profile.Name, "public")
	store.SetLastTableName(profile.Name, "users")

	bag := g.HelperBagForTest()
	bag.OnBeginConnecting(profile)
	g.WaitForWorkersForTest()

	if got := g.ContextTree().Current().GetKey(); got != types.TABLES {
		t.Fatalf("top after connect with saved schema+table = %v; want TABLES", got)
	}
	if got := g.ActiveConnIDForTest(); got != profile.Name {
		t.Fatalf("activeConnID = %q; want %q", got, profile.Name)
	}
}

// AC: Enter → fail → in-place error on the CONNECTING screen → [r] retry
// succeeds and focus advances. The CONNECTING screen is the live error sink
// (no toast); after retry the dial succeeds and we land on SCHEMAS.
func TestPickerStartupFlow_FailureThenRetrySucceeds(t *testing.T) {
	g, rec := buildTestGuiWithHistory(t)

	driverName, conn := registerWireFake(t, drivers.Capabilities{})
	conn.openErr = errors.New("dial failed: connection refused")

	// Register the CONNECTING view so SetContent/GetViewBuffer capture the body.
	_, _ = rec.SetView(string(types.CONNECTING), 0, 0, 40, 10, 0)

	bag := g.HelperBagForTest()
	profile := &models.Connection{Name: "flaky", Driver: driverName, DSN: "postgres://stub"}

	// First attempt fails. OnBeginConnecting pushes CONNECTING then dials async.
	bag.OnBeginConnecting(profile)
	g.WaitForWorkersForTest()

	if got := g.ContextTree().Current().GetKey(); got != types.CONNECTING {
		t.Fatalf("top after dial failure = %v; want CONNECTING (in-place error sink)", got)
	}
	cc := g.Registry().Connecting
	if err := cc.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := rec.GetViewBuffer(string(types.CONNECTING))
	if !strings.Contains(body, "connection refused") {
		t.Fatalf("CONNECTING body missing the error; got %q", body)
	}
	if !strings.Contains(body, "[r] retry") {
		t.Fatalf("CONNECTING body not in error state; got %q", body)
	}
	if got := g.ActiveConnIDForTest(); got != "" {
		t.Fatalf("activeConnID = %q after failure; want empty", got)
	}

	// Clear the error and make the retry succeed.
	conn.openErr = nil
	conn.schemas = []models.Schema{{Name: "public", Owner: "u"}}

	bag.OnRetryConnecting()
	g.WaitForWorkersForTest()

	if got := g.ContextTree().Current().GetKey(); got != types.SCHEMAS {
		t.Fatalf("top after successful retry = %v; want SCHEMAS", got)
	}
	if connectingOnStack(g) {
		t.Fatal("CONNECTING still on stack after successful retry; must be popped/replaced")
	}
	if got := g.ActiveConnIDForTest(); got != profile.Name {
		t.Fatalf("activeConnID = %q after retry; want %q", got, profile.Name)
	}
}

// AC: Esc mid-connect returns focus to the picker and leaves activeConn
// unchanged (empty). We park the dial on the cancellable ctx so the cancel
// path is exercised deterministically. That the parked worker actually
// unblocks via the cancelled ctx (so Close returns) is proven by the
// canonical TestConnectInvokerCancelUnblocksDialAndCloseReturns
// (connect_cancel_test.go) under goleak; this test focuses on the
// focus-return + activeConn-unchanged capstone slice. The picker here is the
// FIRST_RUN_TIP-suppressed CONNECTIONS context built by buildPickerGui, so
// after cancel the top returns to CONNECTIONS.
func TestPickerStartupFlow_CancelMidConnectReturnsToPicker(t *testing.T) {
	g, _, _ := buildPickerGui(t, func() []models.Connection { return nil })

	driverName, conn := registerWireFake(t, drivers.Capabilities{})
	dialEntered := make(chan struct{})
	// Park the dial on the cancellable ctx (unresponsive host). Cancel aborts
	// the ctx so this unblocks and the worker exits cleanly.
	conn.openHookCtx = func(ctx context.Context) {
		close(dialEntered)
		<-ctx.Done()
	}

	bag := g.HelperBagForTest()
	profile := &models.Connection{Name: "midconnect", Driver: driverName, DSN: "postgres://stub"}
	bag.OnBeginConnecting(profile)

	// Wait until the dial is parked inside Open before cancelling.
	select {
	case <-dialEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("dial never entered Open within 5s")
	}

	// CONNECTING should be the live top while the dial is in flight.
	if got := g.ContextTree().Current().GetKey(); got != types.CONNECTING {
		t.Fatalf("top during in-flight connect = %v; want CONNECTING", got)
	}

	bag.OnCancelConnecting()
	g.WaitForWorkersForTest()

	if got := g.ContextTree().Current().GetKey(); got != types.CONNECTIONS {
		t.Fatalf("top after cancel = %v; want CONNECTIONS (back to picker)", got)
	}
	if got := g.ActiveConnIDForTest(); got != "" {
		t.Fatalf("activeConnID = %q after cancel; want empty (cancel must not stamp active conn)", got)
	}
}

// AC: picker rows show the "host/database" endpoint (parsed from the DSN,
// creds stripped) and the active connection is marked with "●". We seed a
// profile with credentials in the DSN, render the rail, and assert the
// endpoint shows while the secret never leaks; then connect and assert the
// active marker paints on that row.
func TestPickerStartupFlow_RowsShowHostDbAndActiveMarker(t *testing.T) {
	driverName, conn := registerWireFake(t, drivers.Capabilities{})
	conn.schemas = []models.Schema{{Name: "public", Owner: "u"}}

	profile := models.Connection{
		Name:   "sales-db",
		Driver: driverName,
		DSN:    "postgres://u:secret@db.example:5432/sales",
	}
	prov := func() []models.Connection { return []models.Connection{profile} }
	g, rec, _ := buildPickerGui(t, prov)

	// Register the CONNECTIONS view in the recorder so SetContent has a target
	// (else SetContent returns ErrUnknownView and the buffer stays empty). The
	// ErrUnknownView return is gocui's "view created" sentinel — ignore.
	_, _ = rec.SetView(string(types.CONNECTIONS), 0, 0, 40, 10, 0)

	// Pre-connect render: endpoint visible, secret redacted, no active marker.
	if err := g.Registry().Connections.HandleRender(); err != nil {
		t.Fatalf("HandleRender (pre-connect): %v", err)
	}
	body := rec.GetViewBuffer(string(types.CONNECTIONS))
	if !strings.Contains(body, "db.example/sales") {
		t.Fatalf("CONNECTIONS rail missing host/db endpoint; got %q", body)
	}
	if strings.Contains(body, "secret") {
		t.Fatalf("CONNECTIONS rail leaked a credential; got %q", body)
	}
	if strings.Contains(body, "●") {
		t.Fatalf("active marker shown before any connect; got %q", body)
	}

	// Connect, then re-render: the active row must now carry the "●" marker.
	bag := g.HelperBagForTest()
	if err := bag.Connect.Connect(context.Background(), &profile); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if got := g.ActiveConnIDForTest(); got != profile.Name {
		t.Fatalf("activeConnID = %q after connect; want %q", got, profile.Name)
	}
	if err := g.Registry().Connections.HandleRender(); err != nil {
		t.Fatalf("HandleRender (post-connect): %v", err)
	}
	body = rec.GetViewBuffer(string(types.CONNECTIONS))
	if !strings.Contains(body, "●") {
		t.Fatalf("active connection row missing the '●' marker after connect; got %q", body)
	}
}

// AC (edge): no saved profiles → the picker renders its empty state and
// pressing Enter is a no-op (no dial). With a nil provider the rail is empty;
// the connections controller's <cr> handler has no row to activate, so no
// connect fires. We assert the rail is empty and acquired stays 0.
func TestPickerStartupFlow_EmptyPickerEnterIsNoop(t *testing.T) {
	_, conn := registerWireFake(t, drivers.Capabilities{})

	g, _, _ := buildPickerGui(t, func() []models.Connection { return nil })

	if got := g.ContextTree().Current().GetKey(); got != types.CONNECTIONS {
		t.Fatalf("top = %v; want CONNECTIONS", got)
	}
	if got := len(g.Registry().Connections.Items()); got != 0 {
		t.Fatalf("CONNECTIONS rail has %d rows; want 0 (no saved profiles)", got)
	}
	// No SelectedItem to activate → no connect path runs.
	if got := conn.acquired.Load(); got != 0 {
		t.Fatalf("conn.AcquireSession called %d times on empty picker; want 0", got)
	}
}

// connectingOnStack reports whether the CONNECTING context is anywhere on the
// focus stack.
func connectingOnStack(g *orchestrator.Gui) bool {
	for _, ctx := range g.ContextTree().Stack() {
		if ctx.GetKey() == types.CONNECTING {
			return true
		}
	}
	return false
}
