package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/query"
)

// connectAndSelectTable wires the fake driver, fires Connect, and seeds
// the TABLES rail with a single *models.Table (selected by default).
// Returns the Gui plus the seeded table.
func connectAndSelectTable(t *testing.T, sch, name string) (*orchestrator.Gui, *models.Table) {
	return connectAndSelectTableOpt(t, sch, name, nil)
}

// connectAndSelectTableOpt is connectAndSelectTable with an optional conn
// mutation applied BEFORE Connect (the fake session caches the conn's error
// fields at AcquireSession time, so per-leaf load failures must be configured
// up front).
func connectAndSelectTableOpt(t *testing.T, sch, name string, opt func(c *wireFakeConn)) (*orchestrator.Gui, *models.Table) {
	t.Helper()
	g, _ := buildTestGuiWithHistory(t)

	caps := drivers.Capabilities{}
	driverName, conn := registerWireFake(t, caps)
	conn.columns = []models.Column{
		{Name: "id"},
		{Name: "email"},
	}
	conn.indexes = []models.Index{
		{Name: "users_pkey", Schema: sch, Table: name},
	}
	if opt != nil {
		opt(conn)
	}

	bag := g.HelperBagForTest()
	profile := &models.Connection{Name: "inspect", Driver: driverName, DSN: "postgres://stub"}
	if err := bag.Connect.Connect(context.Background(), profile); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	tbl := &models.Table{Schema: sch, Name: name}
	g.Registry().Tables.SetItems([]any{tbl})
	g.Registry().Tables.SetCursor(0)
	return g, tbl
}

// fireTableInspectOpen looks up the TableInspectOpen command and invokes
// its handler with a zero ExecCtx. Fails the test on a missing command.
func fireTableInspectOpen(t *testing.T, g *orchestrator.Gui) {
	t.Helper()
	cmd, ok := g.CommandRegistry().Get(commands.TableInspectOpen)
	if !ok || cmd == nil || cmd.Handler == nil {
		t.Fatalf("TableInspectOpen not registered or handler nil")
	}
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("TableInspectOpen handler: %v", err)
	}
}

func TestTableInspectOpen_PushesPopupWhenTableSelected(t *testing.T) {
	g, _ := connectAndSelectTable(t, "public", "users")

	fireTableInspectOpen(t, g)

	top := g.ContextTree().Current()
	if top == nil {
		t.Fatal("focus stack top is nil after TableInspectOpen")
	}
	if got := top.GetKey(); got != types.TABLE_INSPECT {
		t.Fatalf("focus stack top = %q, want %q", got, types.TABLE_INSPECT)
	}
	// SetTarget snapshot landed.
	if sch, name := g.Registry().TableInspect.Target(); sch != "public" || name != "users" {
		t.Fatalf("Target = (%q,%q), want (public,users)", sch, name)
	}
	// Open lands on the first (Columns) tab.
	if got := g.Registry().TableInspect.ActiveTab(); got != 0 {
		t.Fatalf("ActiveTab after open = %d, want 0", got)
	}
}

func TestTableInspectOpen_NoOpWhenNoSelection(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	// No SetItems → SelectedItem() returns nil.
	beforeDepth := len(g.ContextTree().Stack())

	fireTableInspectOpen(t, g)

	afterDepth := len(g.ContextTree().Stack())
	if afterDepth != beforeDepth {
		t.Fatalf("focus stack depth changed: before=%d after=%d (handler should be a no-op)", beforeDepth, afterDepth)
	}
}

func TestTableInspectOpen_DispatchesFiveWorkers(t *testing.T) {
	g, _ := connectAndSelectTable(t, "public", "users")

	before := g.OnWorkerCountForTest()
	fireTableInspectOpen(t, g)
	g.WaitForWorkersForTest()
	after := g.OnWorkerCountForTest()

	if got := after - before; got != 5 {
		t.Fatalf("OnWorker enqueue count = %d, want 5 (columns + indexes + foreign keys + constraints + stats)", got)
	}
}

func TestTableInspectOpen_ClearsLoadingAfterBothRefresh(t *testing.T) {
	g, _ := connectAndSelectTable(t, "public", "users")

	fireTableInspectOpen(t, g)
	g.WaitForWorkersForTest()

	// done() schedules SetLoading(false) on OnUIThreadContentOnly, which
	// the recorder driver dispatches via Update. Assert directly.
	if g.Registry().TableInspect.IsLoading() {
		t.Fatal("TableInspect.IsLoading() = true after both refreshes; expected false")
	}
	if got := len(g.Registry().Columns.Items()); got != 2 {
		t.Fatalf("Columns.Items() = %d, want 2", got)
	}
	if got := len(g.Registry().Indexes.Items()); got != 1 {
		t.Fatalf("Indexes.Items() = %d, want 1", got)
	}
}

func TestTableInspectOpen_ReOpenReTargets(t *testing.T) {
	g, _ := connectAndSelectTable(t, "public", "users")

	fireTableInspectOpen(t, g)
	g.WaitForWorkersForTest()

	depthAfterFirst := len(g.ContextTree().Stack())
	if got := g.ContextTree().Current().GetKey(); got != types.TABLE_INSPECT {
		t.Fatalf("first open: top = %q, want %q", got, types.TABLE_INSPECT)
	}

	// Move off the first tab so the re-open's SetActiveTab(0) is observable.
	g.Registry().TableInspect.SetActiveTab(1)

	// Swap the selected table without closing the popup.
	tblB := &models.Table{Schema: "public", Name: "orders"}
	g.Registry().Tables.SetItems([]any{tblB})
	g.Registry().Tables.SetCursor(0)

	fireTableInspectOpen(t, g)
	g.WaitForWorkersForTest()

	depthAfterSecond := len(g.ContextTree().Stack())
	if depthAfterSecond != depthAfterFirst {
		t.Fatalf("re-open: focus stack depth changed (was %d, now %d) — expected re-target without Push",
			depthAfterFirst, depthAfterSecond)
	}
	if sch, name := g.Registry().TableInspect.Target(); sch != "public" || name != "orders" {
		t.Fatalf("Target after re-open = (%q,%q), want (public,orders)", sch, name)
	}
	// Re-open resets to the first (Columns) tab.
	if got := g.Registry().TableInspect.ActiveTab(); got != 0 {
		t.Fatalf("ActiveTab after re-open = %d, want 0", got)
	}
}

type reviewInspectorSession struct {
	wireFakeSession
	stage   atomic.Int32
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	errA    error
	errB    error
}

func (s *reviewInspectorSession) result(ctx context.Context, table string) (string, error) {
	if s.stage.Load() == 0 {
		return "a_" + table, s.errA
	}
	s.once.Do(func() { close(s.entered) })
	select {
	case <-s.release:
		return "b_" + table, s.errB
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (s *reviewInspectorSession) ListColumns(ctx context.Context, _, table string) ([]models.Column, error) {
	name, err := s.result(ctx, table)
	return []models.Column{{Name: name}}, err
}

func (s *reviewInspectorSession) ListIndexes(ctx context.Context, _, table string) ([]models.Index, error) {
	name, err := s.result(ctx, table)
	return []models.Index{{Name: name}}, err
}

func (s *reviewInspectorSession) ListForeignKeys(ctx context.Context, _, table string) ([]models.ForeignKey, error) {
	name, err := s.result(ctx, table)
	return []models.ForeignKey{{Columns: []string{name}, RefTable: name}}, err
}

func (s *reviewInspectorSession) ListInboundForeignKeys(ctx context.Context, _, table string) ([]models.ForeignKey, error) {
	name, err := s.result(ctx, table)
	return []models.ForeignKey{{Columns: []string{name}, RefTable: name}}, err
}

func (s *reviewInspectorSession) ListConstraints(ctx context.Context, _, table string) ([]models.Constraint, error) {
	name, err := s.result(ctx, table)
	return []models.Constraint{{Name: name, Kind: "CHECK", Definition: name}}, err
}

func (s *reviewInspectorSession) TableStats(ctx context.Context, _, table string) (int64, int64, error) {
	name, err := s.result(ctx, table)
	if strings.HasPrefix(name, "a_") {
		return 111, 111, err
	}
	return 222, 222, err
}

type reviewInspectorConn struct {
	wireFakeConn
	sess *reviewInspectorSession
}

func (c *reviewInspectorConn) AcquireSession(context.Context) (drivers.Session, error) {
	return c.sess, nil
}

type reviewInspectorDriver struct {
	wireFakeDriver
	conn *reviewInspectorConn
}

func (d *reviewInspectorDriver) Open(context.Context, drivers.ConnectionProfile, drivers.ProgressReporter) (drivers.Connection, error) {
	return d.conn, nil
}

func reviewInspectorGui(t *testing.T, errA, errB error) (*orchestrator.Gui, *queueingDriver, *reviewInspectorSession) {
	t.Helper()
	fs := afero.NewMemMapFs()
	store := common.NewAppStateStore(fs, "/tmp/state.yml", common.DefaultClock())
	tmp := t.TempDir()
	g := orchestrator.NewGui(orchestrator.Deps{
		Common: common.NewCommon(slog.New(slog.DiscardHandler), i18n.EnglishTranslationSet(), config.GetDefaultConfig(), &common.AppState{}, fs),
		Store:  store, ConnectionsPath: filepath.Join(tmp, "connections.yml"),
		ConnectionsProvider: func() []models.Connection { return nil },
		HistoryProvider:     func() (*query.History, error) { return query.New(filepath.Join(tmp, "history.sqlite")) },
	})
	d := newQueueingDriver()
	if err := g.UseDriverForTest(d); err != nil {
		t.Fatal(err)
	}
	d.drainPending()
	sess := &reviewInspectorSession{entered: make(chan struct{}), release: make(chan struct{}), errA: errA, errB: errB}
	conn := &reviewInspectorConn{sess: sess}
	name := "review-inspector-" + itoaInt64(uniqueDriverNameCounter.Add(1))
	drivers.Register(name, func(context.Context) (drivers.Driver, error) { return &reviewInspectorDriver{conn: conn}, nil })
	if err := g.HelperBagForTest().Connect.Connect(context.Background(), &models.Connection{Name: name, Driver: name}); err != nil {
		t.Fatal(err)
	}
	g.WaitForWorkersForTest()
	d.drainPending()
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() {
			select {
			case <-sess.release:
			default:
				close(sess.release)
			}
		})
		g.WaitForWorkersForTest()
		d.drainPending()
		_ = g.Close()
	})
	return g, d, sess
}

func reviewInspectorCallbacks(d *queueingDriver) []func() error {
	var callbacks []func() error
	for {
		select {
		case fn := <-d.updates:
			callbacks = append(callbacks, fn)
		default:
			return callbacks
		}
	}
}

func reviewInspectorRun(t *testing.T, callbacks []func() error) {
	t.Helper()
	for _, fn := range callbacks {
		if err := fn(); err != nil {
			t.Errorf("UI callback: %v", err)
		}
	}
}

func reviewInspectorSelect(g *orchestrator.Gui, table *models.Table) {
	g.Registry().Tables.SetItems([]any{table})
	g.Registry().Tables.SetCursor(0)
}

func TestReview_inspector_delayed_publications(t *testing.T) {
	for _, failure := range []struct {
		name string
		err  error
	}{{"success", nil}, {"error", errors.New("synthetic metadata failure")}, {"cancel", context.Canceled}} {
		for _, reopen := range []bool{false, true} {
			for _, aLast := range []bool{false, true} {
				name := fmt.Sprintf("%s/reopen=%t/aLast=%t", failure.name, reopen, aLast)
				t.Run(name, func(t *testing.T) {
					g, d, sess := reviewInspectorGui(t, failure.err, nil)
					a := &models.Table{Schema: "public", Name: "a"}
					reviewInspectorSelect(g, a)
					fireTableInspectOpen(t, g)
					g.WaitForWorkersForTest()
					old := reviewInspectorCallbacks(d)
					if len(old) == 0 {
						t.Fatal("A did not enqueue callbacks")
					}
					b := &models.Table{Schema: "public", Name: "b"}
					if reopen {
						if err := g.ContextTree().Pop(); err != nil {
							t.Fatal(err)
						}
						b = a // same-target, same-pointer reopen still needs a new generation.
					}
					if !g.Registry().TableInspect.IsLoading() {
						t.Fatal("A revealed before its queued acknowledgement")
					}
					if got := a.EstimatedRows.Load(); got != 0 {
						t.Fatalf("stats mutated before UI delivery: %d", got)
					}
					sess.stage.Store(1)
					reviewInspectorSelect(g, b)
					before := g.OnWorkerCountForTest()
					fireTableInspectOpen(t, g)
					if got := g.OnWorkerCountForTest() - before; got != 5 {
						t.Fatalf("B workers = %d, want 5", got)
					}
					select {
					case <-sess.entered:
					case <-time.After(2 * time.Second):
						t.Fatal("B metadata never entered")
					}
					if !aLast {
						reviewInspectorRun(t, old)
						if !g.Registry().TableInspect.IsLoading() {
							t.Error("A acknowledgement cleared B's loading gate")
						}
						if len(g.Registry().Columns.Items()) != 0 || len(g.Registry().Indexes.Items()) != 0 || strings.Contains(g.Registry().ForeignKeys.BodyText(), "a_") || strings.Contains(g.Registry().ForeignKeys.BodyText(), "could not load") || strings.Contains(g.Registry().Constraints.BodyText(), "a_") || strings.Contains(g.Registry().Constraints.BodyText(), "could not load") {
							t.Error("A success/error publication changed B's blank leaves")
						}
						if got := a.EstimatedRows.Load(); got != 0 {
							t.Errorf("stale A stats published: %d", got)
						}
					}
					close(sess.release)
					g.WaitForWorkersForTest()
					fresh := reviewInspectorCallbacks(d)
					var reveals int
					for _, fn := range fresh {
						loading := g.Registry().TableInspect.IsLoading()
						reviewInspectorRun(t, []func() error{fn})
						if loading && !g.Registry().TableInspect.IsLoading() {
							reveals++
						}
					}
					if aLast {
						reviewInspectorRun(t, old)
					}
					if reveals != 1 {
						t.Errorf("B reveal transitions = %d, want exactly 1", reveals)
					}
					if g.Registry().TableInspect.IsLoading() {
						t.Error("B did not settle")
					}
					marker := "b_" + b.Name
					cols, indexes := g.Registry().Columns.Items(), g.Registry().Indexes.Items()
					if len(cols) != 1 || cols[0].(models.Column).Name != marker || len(indexes) != 1 || indexes[0].(models.Index).Name != marker {
						t.Errorf("B columns/indexes replaced: %v / %v", cols, indexes)
					}
					for leaf, body := range map[string]string{"fk": g.Registry().ForeignKeys.BodyText(), "constraint": g.Registry().Constraints.BodyText()} {
						if !strings.Contains(body, marker) || strings.Contains(body, "a_") || strings.Contains(body, "could not load") {
							t.Errorf("B %s replaced: %q", leaf, body)
						}
					}
					if got := b.EstimatedRows.Load(); got != 222 {
						t.Errorf("B stats = %d, want 222", got)
					}
				})
			}
		}
	}
}
