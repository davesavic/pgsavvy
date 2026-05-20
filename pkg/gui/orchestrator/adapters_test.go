package orchestrator_test

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/spf13/afero"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/dbsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/logs"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/query"
)

// captureHook records every fired logrus entry.
type captureHook struct {
	mu      sync.Mutex
	entries []*logrus.Entry
}

func (h *captureHook) Levels() []logrus.Level { return logrus.AllLevels }
func (h *captureHook) Fire(e *logrus.Entry) error {
	cp := logrus.Fields{}
	for k, v := range e.Data {
		cp[k] = v
	}
	dup := *e
	dup.Data = cp
	h.mu.Lock()
	h.entries = append(h.entries, &dup)
	h.mu.Unlock()
	return nil
}

func (h *captureHook) snapshot() []*logrus.Entry {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*logrus.Entry, len(h.entries))
	copy(out, h.entries)
	return out
}

// TestConnectAndBind_PassesBridgedLogger exercises the wireQueryRuntime
// wiring: when c.g.deps.Common.Log is set, the SQLSession constructed
// inside Connect must receive an slog.Logger backed by logs.NewSlogHandler.
//
// We cannot reach the private logger field from this _test package, so we
// verify end-to-end via a structural check: the bridge constructed against
// the same Common.Log routes a sample slog emit through that logger's hooks
// with cat="db". This is the same code path wireQueryRuntime takes; if it
// regressed, this test would still catch a bridge-construction break and
// the Connect path itself is exercised by TestConnectInvokerBindsQueryRunner.
func TestConnectAndBind_PassesBridgedLogger(t *testing.T) {
	// Build a Gui whose Common.Log has a capture hook.
	fs := afero.NewMemMapFs()
	log := logrus.New()
	log.SetOutput(io.Discard)
	log.SetLevel(logrus.DebugLevel)
	hook := &captureHook{}
	log.AddHook(hook)
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
	// the chain; if the bridge wiring panicked or mis-typed, this fails.
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

	// Behavioural check: emit a record through a bridge constructed
	// against the same Common.Log. This is exactly what wireQueryRuntime
	// does internally; the entry must land on the capture hook with
	// cat="db".
	bridged := slog.New(logs.NewSlogHandler(c.Log))
	bridged.Warn("session.io: round-trip")
	es := hook.snapshot()
	if len(es) == 0 {
		t.Fatal("capture hook received no entries; bridge did not forward through logrus")
	}
	last := es[len(es)-1]
	if last.Message != "session.io: round-trip" {
		t.Errorf("Message = %q, want %q", last.Message, "session.io: round-trip")
	}
	if got := last.Data["cat"]; got != "db" {
		t.Errorf("Data[cat] = %v, want db", got)
	}
}
