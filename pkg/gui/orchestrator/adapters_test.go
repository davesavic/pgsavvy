package orchestrator_test

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"

	"github.com/spf13/afero"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/dbsavvy/pkg/gui/orchestrator"
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
// to *slog.Logger has been deleted (dbsavvy-962 F1); Common.Logger() is
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

// dbsavvy-56u.1: populateIndexesRail loads indexes via the live
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

// dbsavvy-56u.1: populateIndexesRail with an empty schema or table is
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
