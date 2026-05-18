package orchestrator_test

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/sirupsen/logrus"
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

// --- minimal fakes for the wireQueryRuntime path -------------------------
//
// The Connect path used here invokes:
//   ConnectHelper.Connect -> driver.Open / conn.AcquireSession (first session)
//   connectInvoker.wireQueryRuntime -> conn.AcquireSession (second session)
//   session.New(conn, inner, opts) -> inner.ID()
// Nothing else fires until the test calls QueryRunner.Run, so the fakes can
// stub every other Session/Connection method with zero values.

type wireFakeSession struct {
	id models.SessionID
}

func (s *wireFakeSession) Close() error         { return nil }
func (s *wireFakeSession) ID() models.SessionID { return s.id }
func (s *wireFakeSession) ListDatabases(_ context.Context) ([]models.Database, error) {
	return nil, nil
}

func (s *wireFakeSession) ListSchemas(_ context.Context, _ string) ([]models.Schema, error) {
	return nil, nil
}

func (s *wireFakeSession) ListTables(_ context.Context, _ string) ([]*models.Table, error) {
	return nil, nil
}

func (s *wireFakeSession) ListColumns(_ context.Context, _, _ string) ([]models.Column, error) {
	return nil, nil
}

func (s *wireFakeSession) ListIndexes(_ context.Context, _, _ string) ([]models.Index, error) {
	return nil, nil
}

func (s *wireFakeSession) ListConstraints(_ context.Context, _, _ string) ([]models.Constraint, error) {
	return nil, nil
}

func (s *wireFakeSession) DescribeFunction(_ context.Context, _, _ string) (models.FunctionDetail, error) {
	return models.FunctionDetail{}, nil
}

func (s *wireFakeSession) Execute(_ context.Context, _ models.Query) (models.Result, error) {
	return models.Result{}, nil
}

func (s *wireFakeSession) Stream(_ context.Context, _ models.Query) (drivers.RowStream, error) {
	return nil, nil
}

func (s *wireFakeSession) Explain(_ context.Context, _ models.Query, _ bool) (models.Plan, error) {
	return models.Plan{}, nil
}

func (s *wireFakeSession) Begin(_ context.Context, _ models.TxOptions) (drivers.Transaction, error) {
	return nil, nil
}
func (s *wireFakeSession) InTransaction() bool                     { return false }
func (s *wireFakeSession) CurrentTransaction() drivers.Transaction { return nil }

type wireFakeConn struct {
	acquired atomic.Int32
}

func (c *wireFakeConn) Close() error                                     { return nil }
func (c *wireFakeConn) Ping(_ context.Context) error                     { return nil }
func (c *wireFakeConn) ServerVersion() string                            { return "fake-wire-1.0" }
func (c *wireFakeConn) Cancel(_ context.Context, _ models.QueryID) error { return nil }
func (c *wireFakeConn) AcquireSession(_ context.Context) (drivers.Session, error) {
	n := c.acquired.Add(1)
	return &wireFakeSession{id: models.SessionID(n)}, nil
}

type wireFakeDriver struct {
	conn *wireFakeConn
	caps drivers.Capabilities
}

func (d *wireFakeDriver) Name() string                       { return "wire-fake" }
func (d *wireFakeDriver) Capabilities() drivers.Capabilities { return d.caps }
func (d *wireFakeDriver) Open(_ context.Context, _ drivers.ConnectionProfile) (drivers.Connection, error) {
	return d.conn, nil
}

// uniqueDriverNameCounter avoids cross-test collisions: drivers.Register
// panics on duplicate names and the registry has no public Unregister.
var uniqueDriverNameCounter atomic.Int64

func registerWireFake(t *testing.T, caps drivers.Capabilities) (name string, conn *wireFakeConn) {
	t.Helper()
	n := uniqueDriverNameCounter.Add(1)
	name = "wire-fake-" + itoaInt64(n)
	conn = &wireFakeConn{}
	drv := &wireFakeDriver{conn: conn, caps: caps}
	drivers.Register(name, func(_ context.Context) (drivers.Driver, error) {
		return drv, nil
	})
	return name, conn
}

// itoaInt64 avoids dragging strconv into the rest of the file's import list.
func itoaInt64(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// buildTestGuiWithHistory mirrors buildTestGui (gui_test.go) but injects
// a HistoryProvider that opens the sqlite under t.TempDir() so the XDG
// state dir is never touched during tests.
func buildTestGuiWithHistory(t *testing.T) (*orchestrator.Gui, *testfake.RecorderGuiDriver) {
	t.Helper()
	fs := afero.NewMemMapFs()
	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)
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
	return g, rec
}

func TestQueryRunnerHasNoSessionBeforeConnect(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)
	bag := g.HelperBagForTest()
	if bag.QueryRunner == nil {
		t.Fatal("HelperBag.QueryRunner is nil after wireWithDriver — wiring did not stash the runner")
	}
	if bag.QueryRunner.HasSession() {
		t.Fatal("HasSession() = true before Connect; expected false")
	}
	if g.ActiveSQLSessionForTest() != nil {
		t.Fatal("ActiveSQLSessionForTest() != nil before Connect; expected nil")
	}
}

func TestConnectInvokerBindsQueryRunner(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	caps := drivers.Capabilities{HasLiveCancel: true, MaxIdentifierLen: 63}
	driverName, conn := registerWireFake(t, caps)

	bag := g.HelperBagForTest()
	if bag.Connect == nil {
		t.Fatal("HelperBag.Connect is nil")
	}

	profile := &models.Connection{Name: "wired", Driver: driverName, DSN: "postgres://stub"}
	if err := bag.Connect.Connect(context.Background(), profile); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Connect path acquires TWO sessions: the schema-rail session from
	// ConnectHelper and the query-runtime session from wireQueryRuntime.
	if got := conn.acquired.Load(); got != 2 {
		t.Fatalf("conn.AcquireSession called %d times, want 2 (schema + query)", got)
	}

	// The runner pointer the helperBag holds is the SAME *data.QueryRunner
	// that wireWithDriver stashed on the Gui — Bind on the orchestrator's
	// reference is observable here.
	if !bag.QueryRunner.HasSession() {
		t.Fatal("HasSession() = false after Connect; Bind did not propagate")
	}
	if got := bag.QueryRunner.Capabilities(); got != caps {
		t.Fatalf("Capabilities() = %+v, want %+v", got, caps)
	}
	if g.ActiveSQLSessionForTest() == nil {
		t.Fatal("ActiveSQLSessionForTest() = nil after Connect; SQLSession was not stashed for Close")
	}
}

func TestEditorBufferReaderReadsViewBuffer(t *testing.T) {
	g, rec := buildTestGuiWithHistory(t)

	// Seed the QUERY_EDITOR view's buffer via the recorder driver. The
	// recorder driver returns nil for ViewByName even on existing views,
	// so CursorOffset's defensive nil-View branch is the path exercised
	// here — we assert it returns 0 instead of panicking. SetView returns
	// gocui.ErrUnknownView on first creation (per gocui contract); the
	// recorder driver still installs the view, so we swallow that one
	// expected error and surface anything else.
	_, _ = rec.SetView("query_editor", 0, 0, 80, 24, 0)
	if err := rec.SetContent("query_editor", "SELECT 1;\nSELECT 2;"); err != nil {
		t.Fatalf("SetContent: %v", err)
	}

	bag := g.HelperBagForTest()
	if bag.EditorBuffer == nil {
		t.Fatal("HelperBag.EditorBuffer is nil after wireWithDriver")
	}
	if got, want := bag.EditorBuffer.BufferText(), "SELECT 1;\nSELECT 2;"; got != want {
		t.Fatalf("BufferText() = %q, want %q", got, want)
	}
	// Recorder driver returns nil View; CursorOffset must clamp to 0 not panic.
	if got := bag.EditorBuffer.CursorOffset(); got != 0 {
		t.Fatalf("CursorOffset() with nil-View recorder = %d, want 0", got)
	}
}
