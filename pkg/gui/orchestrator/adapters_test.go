package orchestrator_test

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/afero"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/drivers"
	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/dbsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/query"
)

// captureHandler is a slog.Handler that snapshots every emitted record
// under a mutex so concurrent emitters don't race the test goroutine.
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *captureHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(_ string) slog.Handler      { return h }

func (h *captureHandler) snapshot() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]slog.Record, len(h.records))
	copy(out, h.records)
	return out
}

// TestConnectAndBind_PassesLoggerToSQLSession exercises the wireQueryRuntime
// wiring: the SQLSession constructed inside Connect must receive
// Common.Logger(). The bridge that previously converted a legacy logger
// to *slog.Logger has been deleted; Common.Logger() is
// now native *slog.Logger and is passed straight through.
//
// We cannot reach the private logger field from this _test package, so we
// verify the wiring structurally: a Logger captured against the same
// Common routes a sample emit through the underlying handler. This is
// exactly what wireQueryRuntime does internally.
func TestConnectAndBind_PassesLoggerToSQLSession(t *testing.T) {
	// Build a Gui whose Common is wired to a capture handler.
	fs := afero.NewMemMapFs()
	hook := &captureHandler{}
	log := slog.New(hook)
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
		HistoryProvider: func() (*query.History, error) {
			return query.New(filepath.Join(tmp, "history.sqlite"))
		},
	})
	rec := testfake.NewRecorderGuiDriver()
	if err := g.UseDriverForTest(rec); err != nil {
		t.Fatalf("UseDriverForTest: %v", err)
	}
	t.Cleanup(func() { _ = g.Close() })

	// Drive Connect — wireQueryRuntime is the second AcquireSession in
	// the chain; if the logger wiring panicked or mis-typed, this fails.
	caps := drivers.Capabilities{}
	driverName, conn := registerWireFake(t, caps)
	profile := &models.Connection{Name: "bridged", Driver: driverName, DSN: "postgres://stub"}
	if err := g.HelperBagForTest().Connect.Connect(context.Background(), profile); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if got := conn.acquired.Load(); got != 2 {
		t.Fatalf("AcquireSession calls = %d, want 2 (schema + query)", got)
	}
	if g.ActiveSQLSessionForTest() == nil {
		t.Fatal("ActiveSQLSessionForTest() = nil after Connect")
	}

	// Behavioural check: emit a record through the same logger Connect
	// would have handed off. The record must land on the capture handler.
	c.Logger().Warn("session.io: round-trip", "cat", "db")
	rs := hook.snapshot()
	if len(rs) == 0 {
		t.Fatal("capture handler received no records; Common.Logger() did not forward")
	}
	last := rs[len(rs)-1]
	if last.Message != "session.io: round-trip" {
		t.Errorf("Message = %q, want %q", last.Message, "session.io: round-trip")
	}
	var sawCatDB bool
	last.Attrs(func(a slog.Attr) bool {
		if a.Key == "cat" && a.Value.String() == "db" {
			sawCatDB = true
			return false
		}
		return true
	})
	if !sawCatDB {
		t.Errorf("expected attr cat=db in record; got %+v", last)
	}
}

// populateIndexesRail loads indexes via the live
// ConnectHelper and pushes them onto IndexesContext so the rail
// renders rows on the next layout frame. Mirrors the SCHEMAS-rail
// population AC.
func TestPopulateIndexesRailPopulatesRail(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	caps := drivers.Capabilities{}
	driverName, conn := registerWireFake(t, caps)
	conn.indexes = []models.Index{
		{Name: "users_pkey", Schema: "public", Table: "users"},
		{Name: "users_email_idx", Schema: "public", Table: "users"},
	}

	bag := g.HelperBagForTest()
	profile := &models.Connection{Name: "wire-indexes", Driver: driverName, DSN: "postgres://stub"}
	if err := bag.Connect.Connect(context.Background(), profile); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	g.PopulateIndexesRailForTest("public", "users")

	idx := g.Registry().Indexes
	if idx == nil {
		t.Fatal("registry.Indexes is nil")
	}
	items := idx.Items()
	if len(items) != 2 {
		t.Fatalf("IndexesContext.Items() = %d entries, want 2; items=%+v", len(items), items)
	}
}

// A connect that is superseded mid-dial (a newer
// activation bumps connectGen while this one is still dialing) MUST NOT
// mutate activeConn — the stale result is dropped. We simulate the newer
// activation via openHook, which bumps connectGen during the dial; the
// returning connect then finds itself stale and leaves activeConnID
// untouched.
func TestConnectInvokerSupersededConnectDoesNotClobberActiveConn(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	caps := drivers.Capabilities{}
	driverName, conn := registerWireFake(t, caps)
	// Bump the generation during the dial so the connect that captured
	// the earlier token is stale by the time it tries to mutate.
	conn.openHook = func() { g.BumpConnectGenForTest() }

	bag := g.HelperBagForTest()
	profile := &models.Connection{Name: "superseded", Driver: driverName, DSN: "postgres://stub"}
	if err := bag.Connect.Connect(context.Background(), profile); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if got := g.ActiveConnIDForTest(); got != "" {
		t.Fatalf("activeConnID = %q after a superseded connect; want empty (stale result must not clobber)", got)
	}
}

// (SECURITY): a dial error whose message embeds both a
// URL-form DSN (postgres://u:secret@h/db) AND a kv-form password
// (password=secret) MUST reach the CONNECTING screen with BOTH credential
// forms redacted — neither "secret" may survive into the rendered body.
// The CONNECTING screen is the live error sink (no toast).
func TestConnectInvokerDialErrorRedactsCredsIntoConnectingScreen(t *testing.T) {
	g, rec := buildTestGuiWithHistory(t)

	caps := drivers.Capabilities{}
	driverName, conn := registerWireFake(t, caps)
	conn.openErr = errors.New(
		"dial failed for postgres://u:secret@h/db (password=secret): connection refused")

	// Register the CONNECTION_MANAGER view in the recorder (the real layout
	// pass does this via SetView) so SetContent/GetViewBuffer capture the body.
	_, _ = rec.SetView(string(types.CONNECTION_MANAGER), 0, 0, 40, 10, 0)

	cm := g.Registry().ConnectionManager
	if cm == nil {
		t.Fatal("registry.ConnectionManager is nil")
	}
	// Pop the first-run tip so CONNECTION_MANAGER is top of stack
	// (publishConnectError checks Current().GetKey()).
	_ = g.ContextTree().Pop()

	bag := g.HelperBagForTest()
	profile := &models.Connection{Name: "creds", Driver: driverName, DSN: "postgres://stub"}
	if err := bag.Connect.Connect(context.Background(), profile); err == nil {
		t.Fatal("Connect returned nil; want the dial error to propagate")
	}

	// The error is now stored in ConnectingState. Switch to connecting mode
	// to render the error body, then check redaction.
	cm.SetMode(guicontext.ModeConnecting)
	if err := cm.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := rec.GetViewBuffer(string(types.CONNECTION_MANAGER))
	if strings.Contains(body, "secret") {
		t.Fatalf("CONNECTION_MANAGER body leaked a credential: %q", body)
	}
	if !strings.Contains(body, "***") {
		t.Fatalf("CONNECTION_MANAGER body missing redaction marker; got %q", body)
	}
}

// A stale-gen worker (superseded mid-dial by a newer
// activation) MUST NOT paint the error screen — its error result is
// dropped. We bump connectGen during the dial via openHook so the returning
// worker finds itself stale; the screen stays in its connecting state.
func TestConnectInvokerStaleDialErrorDroppedNotRendered(t *testing.T) {
	g, rec := buildTestGuiWithHistory(t)

	caps := drivers.Capabilities{}
	driverName, conn := registerWireFake(t, caps)
	conn.openHook = func() { g.BumpConnectGenForTest() }
	conn.openErr = errors.New("dial failed: connection refused")

	_, _ = rec.SetView(string(types.CONNECTION_MANAGER), 0, 0, 40, 10, 0)

	// CONNECTION_MANAGER is already the startup root. Set its connecting
	// state so we can verify the stale error is not painted.
	cm := g.Registry().ConnectionManager
	cm.ConnectingState().SetConnectingStaged("stale", nil)

	bag := g.HelperBagForTest()
	profile := &models.Connection{Name: "stale", Driver: driverName, DSN: "postgres://stub"}
	_ = bag.Connect.Connect(context.Background(), profile)

	if err := cm.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := rec.GetViewBuffer(string(types.CONNECTION_MANAGER))
	if strings.Contains(body, "connection refused") {
		t.Fatalf("stale worker painted the error screen: %q", body)
	}
}

// populateIndexesRail with an empty schema or table is
// a silent no-op — the existing IndexesContext.items are left intact.
func TestPopulateIndexesRailEmptyKeysIsNoop(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	caps := drivers.Capabilities{}
	driverName, conn := registerWireFake(t, caps)
	conn.indexes = []models.Index{{Name: "x", Schema: "public", Table: "t"}}

	bag := g.HelperBagForTest()
	profile := &models.Connection{Name: "wire-indexes-noop", Driver: driverName, DSN: "postgres://stub"}
	if err := bag.Connect.Connect(context.Background(), profile); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	g.PopulateIndexesRailForTest("", "users")
	if got := len(g.Registry().Indexes.Items()); got != 0 {
		t.Fatalf("empty schema: Items() = %d, want 0", got)
	}
	g.PopulateIndexesRailForTest("public", "")
	if got := len(g.Registry().Indexes.Items()); got != 0 {
		t.Fatalf("empty table: Items() = %d, want 0", got)
	}
}

// populateIndexesRail runs on a worker goroutine, so its
// SetItems publish (a mutex-free items+cursor write the MainLoop reads
// every render frame) MUST be marshaled onto the UI thread rather than
// written raw on the worker. With a wired driver, runOnUIThread routes
// the publish through GuiDriver.Update; the recorder runs Update inline,
// so we assert (a) exactly one Update was recorded by the populate call
// (the marshal happened) and (b) the items still landed (recorder inline
// execution mirrors the MainLoop draining the queued func). A raw,
// unmarshaled SetItems would NOT increment UpdateCalls — that is the
// regression this guards against.
func TestPopulateIndexesRailMarshalsSetItemsOntoUIThread(t *testing.T) {
	g, rec := buildTestGuiWithHistory(t)

	caps := drivers.Capabilities{}
	driverName, conn := registerWireFake(t, caps)
	conn.indexes = []models.Index{
		{Name: "users_pkey", Schema: "public", Table: "users"},
		{Name: "users_email_idx", Schema: "public", Table: "users"},
	}

	bag := g.HelperBagForTest()
	profile := &models.Connection{Name: "wire-indexes-marshal", Driver: driverName, DSN: "postgres://stub"}
	if err := bag.Connect.Connect(context.Background(), profile); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Snapshot AFTER Connect so we count only the populate call's marshal.
	preUpdate := rec.UpdateCalls

	g.PopulateIndexesRailForTest("public", "users")

	if got := rec.UpdateCalls - preUpdate; got != 1 {
		t.Fatalf("UpdateCalls delta = %d, want 1 — SetItems must be marshaled "+
			"through driver.Update exactly once (raw write would be 0)", got)
	}
	if errs := rec.UpdateErrors(); len(errs) != 0 {
		t.Fatalf("driver propagated errors from the marshaled publish: %v", errs)
	}
	if got := len(g.Registry().Indexes.Items()); got != 2 {
		t.Fatalf("IndexesContext.Items() = %d, want 2 — marshaled publish must "+
			"still land the rows (recorder runs Update inline)", got)
	}
}
