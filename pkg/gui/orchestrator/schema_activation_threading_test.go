package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/query"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
)

type reviewSchemaSession struct {
	wireFakeSession
	stage         atomic.Int32
	setError      bool
	tableErr      bool
	block         bool
	entered       chan struct{}
	release       chan struct{}
	canceled      chan struct{}
	once          sync.Once
	listSchemasFn func(context.Context, string) ([]models.Schema, error)
	listTablesFn  func(context.Context, string) ([]*models.Table, error)
}

func (s *reviewSchemaSession) Execute(ctx context.Context, q models.Query) (models.Result, error) {
	if s.stage.Load() == 1 && strings.HasPrefix(q.SQL, "SET search_path") {
		if s.block {
			s.once.Do(func() { close(s.entered) })
			select {
			case <-ctx.Done():
				close(s.canceled)
				return models.Result{}, ctx.Err()
			case <-s.release:
			}
		}
		if s.setError {
			return models.Result{}, errors.New("synthetic SET failure")
		}
	}
	return models.Result{}, nil
}

func (s *reviewSchemaSession) ListSchemas(ctx context.Context, database string) ([]models.Schema, error) {
	if s.listSchemasFn != nil {
		return s.listSchemasFn(ctx, database)
	}
	return []models.Schema{{Name: "a"}, {Name: "b"}}, nil
}

func (s *reviewSchemaSession) ListTables(ctx context.Context, schema string) ([]*models.Table, error) {
	if s.listTablesFn != nil {
		return s.listTablesFn(ctx, schema)
	}
	stage := s.stage.Load()
	if stage == 1 && s.tableErr {
		return nil, errors.New("synthetic table failure")
	}
	return []*models.Table{{Schema: schema, Name: fmt.Sprintf("stage%d_%s", stage, schema)}}, nil
}

type reviewSchemaConn struct {
	wireFakeConn
	sess *reviewSchemaSession
}

func (c *reviewSchemaConn) AcquireSession(context.Context) (drivers.Session, error) {
	return c.sess, nil
}

type reviewSchemaDriver struct {
	wireFakeDriver
	conn *reviewSchemaConn
}

func (d *reviewSchemaDriver) Open(context.Context, drivers.ConnectionProfile, drivers.ProgressReporter) (drivers.Connection, error) {
	return d.conn, nil
}

func reviewSchemaGui(t *testing.T, sess *reviewSchemaSession) (*orchestrator.Gui, *queueingDriver, *common.AppStateStore, *models.Connection) {
	t.Helper()
	fs := afero.NewMemMapFs()
	store := common.NewAppStateStore(fs, "/tmp/schema-state.yml", common.DefaultClock())
	tmp := t.TempDir()
	g := orchestrator.NewGui(orchestrator.Deps{
		Common: common.NewCommon(slog.New(slog.DiscardHandler), i18n.EnglishTranslationSet(), config.GetDefaultConfig(), &common.AppState{}, fs),
		Store:  store, ConnectionsPath: filepath.Join(tmp, "connections.yml"),
		ConnectionsProvider: func() []models.Connection { return nil },
		HistoryProvider:     func() (*query.History, error) { return query.New(filepath.Join(tmp, "history.sqlite")) },
	})
	d := newQueueingDriver()
	require.NoError(t, g.UseDriverForTest(d))
	d.drainPending()
	name := "review-schema-" + itoaInt64(uniqueDriverNameCounter.Add(1))
	conn := &reviewSchemaConn{sess: sess}
	drivers.Register(name, func(context.Context) (drivers.Driver, error) { return &reviewSchemaDriver{conn: conn}, nil })
	profile := &models.Connection{Name: "schema-profile", Driver: name}
	require.NoError(t, g.HelperBagForTest().Connect.Connect(context.Background(), profile))
	g.WaitForWorkersForTest()
	d.drainPending()
	g.WaitForWorkersForTest()
	d.drainPending()
	t.Cleanup(func() {
		if sess.release != nil {
			select {
			case <-sess.release:
			default:
				close(sess.release)
			}
		}
		g.WaitForWorkersForTest()
		d.drainPending()
		g.WaitForWorkersForTest()
		d.drainPending()
		require.NoError(t, g.Close())
	})
	g.Registry().SchemaRail.SetActiveTab(guicontext.SchemaRailTabSchemas)
	require.NoError(t, g.ContextTree().Push(g.Registry().SchemaRail))
	return g, d, store, profile
}

func reviewSchemaActivate(t *testing.T, g *orchestrator.Gui, schema string) {
	t.Helper()
	for i, item := range g.Registry().Schemas.Items() {
		if item.(models.Schema).Name == schema {
			g.Registry().Schemas.SetCursor(i)
			cmd, ok := g.CommandRegistry().Get(commands.ListConfirm + ":" + string(types.SCHEMAS))
			require.True(t, ok)
			require.NoError(t, cmd.Handler(commands.ExecCtx{}))
			return
		}
	}
	t.Fatalf("schema %s missing", schema)
}

func TestReview_schema_ui_delayed(t *testing.T) {
	for _, scenario := range []string{"success", "set_error", "table_error", "superseded", "same_schema", "a_b_a", "connection_generation", "helper_reconnect", "close"} {
		t.Run(scenario, func(t *testing.T) {
			sess := &reviewSchemaSession{setError: scenario == "set_error", tableErr: scenario == "table_error"}
			g, d, store, _ := reviewSchemaGui(t, sess)
			before := []*models.Table{{Schema: "old", Name: "old_table"}}
			g.Registry().Tables.SetItems([]any{before[0]})
			sess.stage.Store(1)
			reviewSchemaActivate(t, g, "a")
			g.WaitForWorkersForTest()
			old := reviewInspectorCallbacks(d)
			t.Run("worker_affinity", func(t *testing.T) {
				require.Equal(t, guicontext.SchemaRailTabSchemas, g.Registry().SchemaRail.ActiveTab(), "worker must not switch tab")
				require.Equal(t, []any{before[0]}, g.Registry().Tables.Items(), "worker must not publish rows")
			})
			want := "stage1_a"
			switch scenario {
			case "superseded", "same_schema", "a_b_a":
				sess.stage.Store(2)
				schema := "b"
				if scenario == "same_schema" {
					schema = "a"
				}
				reviewSchemaActivate(t, g, schema)
				g.WaitForWorkersForTest()
				if scenario == "a_b_a" {
					sess.stage.Store(3)
					reviewSchemaActivate(t, g, "a")
					g.WaitForWorkersForTest()
					schema = "a"
				}
				d.drainPending()
				want = fmt.Sprintf("stage%d_%s", sess.stage.Load(), schema)
				require.Equal(t, schema, store.LastSchemaNameSnapshot("schema-profile"))
			case "connection_generation":
				g.BumpConnectGenForTest()
				want = "old_table"
			case "helper_reconnect":
				sess.stage.Store(2)
				g.ActiveSQLSessionForTest().SetDisconnected(true)
				cmd, ok := g.CommandRegistry().Get(commands.Reconnect)
				require.True(t, ok)
				require.NoError(t, cmd.Handler(commands.ExecCtx{}))
				g.WaitForWorkersForTest()
				d.drainPending()
				want = g.Registry().Tables.Items()[0].(*models.Table).Name
				g.Registry().SchemaRail.SetActiveTab(guicontext.SchemaRailTabSchemas)
			case "close":
				require.NoError(t, g.Close())
				want = "old_table"
			case "table_error":
				want = "old_table"
			}
			reviewInspectorRun(t, old)
			require.Equal(t, want, g.Registry().Tables.Items()[0].(*models.Table).Name)
			if scenario == "connection_generation" || scenario == "helper_reconnect" || scenario == "close" {
				require.Equal(t, guicontext.SchemaRailTabSchemas, g.Registry().SchemaRail.ActiveTab())
			} else {
				require.Equal(t, guicontext.SchemaRailTabTables, g.Registry().SchemaRail.ActiveTab())
				require.Equal(t, types.SCHEMA_RAIL, g.ContextTree().Current().GetKey())
			}
		})
	}
}

func TestReview_schema_ui_cancel_loading(t *testing.T) {
	for _, closeGui := range []bool{false, true} {
		t.Run(fmt.Sprint(closeGui), func(t *testing.T) {
			sess := &reviewSchemaSession{block: true, entered: make(chan struct{}), release: make(chan struct{}), canceled: make(chan struct{})}
			g, d, _, _ := reviewSchemaGui(t, sess)
			sess.stage.Store(1)
			reviewSchemaActivate(t, g, "a")
			select {
			case <-sess.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("SET did not enter")
			}
			if closeGui {
				require.NoError(t, g.Close())
			} else {
				sess.stage.Store(2)
				reviewSchemaActivate(t, g, "b")
			}
			select {
			case <-sess.canceled:
			case <-time.After(time.Second):
				t.Error("supersession/shutdown did not cancel SET")
				close(sess.release)
			}
			g.WaitForWorkersForTest()
			d.drainPending()
			if !closeGui {
				require.Equal(t, "stage2_b", g.Registry().Tables.Items()[0].(*models.Table).Name)
			}
		})
	}
}

func TestReview_schema_ui_pending_connection_publication(t *testing.T) {
	sess := &reviewSchemaSession{}
	g, d, _, _ := reviewSchemaGui(t, sess)
	oldSQL := g.ActiveSQLSessionForTest()
	oldSQL.SetDisconnected(true)
	cmd, ok := g.CommandRegistry().Get(commands.Reconnect)
	require.True(t, ok)
	require.NoError(t, cmd.Handler(commands.ExecCtx{}))
	g.WaitForWorkersForTest()
	connectionCallbacks := reviewInspectorCallbacks(d)
	require.NotEmpty(t, connectionCallbacks)
	require.Same(t, oldSQL, g.ActiveSQLSessionForTest(), "runtime publication must still be pending")
	sess.stage.Store(1)
	reviewSchemaActivate(t, g, "a")
	g.WaitForWorkersForTest()
	activationCallbacks := reviewInspectorCallbacks(d)
	require.NotEmpty(t, activationCallbacks)
	reviewInspectorRun(t, connectionCallbacks)
	require.NotSame(t, oldSQL, g.ActiveSQLSessionForTest())
	newRows := []any{&models.Table{Schema: "b", Name: "new_connection_rows"}}
	g.Registry().Tables.SetItems(newRows)
	g.Registry().SchemaRail.SetActiveTab(guicontext.SchemaRailTabSchemas)
	require.NoError(t, g.ContextTree().Push(g.Registry().QueryRail))
	reviewInspectorRun(t, activationCallbacks)
	require.Equal(t, newRows, g.Registry().Tables.Items())
	require.Equal(t, guicontext.SchemaRailTabSchemas, g.Registry().SchemaRail.ActiveTab())
	require.Equal(t, types.QUERY_RAIL, g.ContextTree().Current().GetKey())
}

type reviewRefreshReply struct {
	names []string
	err   error
}

type reviewRefreshLoad struct {
	tab    int
	schema string
	ctx    context.Context
	reply  chan reviewRefreshReply
}

func reviewRefreshFixture(t *testing.T) (*orchestrator.Gui, *queueingDriver, <-chan reviewRefreshLoad, func()) {
	t.Helper()
	sess := &reviewSchemaSession{}
	calls := make(chan reviewRefreshLoad, 32)
	stop := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(stop) }) }
	load := func(ctx context.Context, tab int, schema string) reviewRefreshReply {
		if sess.stage.Load() == 0 {
			return reviewRefreshReply{names: []string{"a", "b", "c"}}
		}
		call := reviewRefreshLoad{tab: tab, schema: schema, ctx: ctx, reply: make(chan reviewRefreshReply, 1)}
		select {
		case calls <- call:
		case <-stop:
			return reviewRefreshReply{err: context.Canceled}
		case <-ctx.Done():
			return reviewRefreshReply{err: ctx.Err()}
		}
		select {
		case result := <-call.reply:
			return result
		case <-stop:
			return reviewRefreshReply{err: context.Canceled}
		case <-ctx.Done():
			return reviewRefreshReply{err: ctx.Err()}
		}
	}
	sess.listSchemasFn = func(ctx context.Context, _ string) ([]models.Schema, error) {
		result := load(ctx, guicontext.SchemaRailTabSchemas, "")
		items := make([]models.Schema, len(result.names))
		for i, name := range result.names {
			items[i] = models.Schema{Name: name}
		}
		return items, result.err
	}
	sess.listTablesFn = func(ctx context.Context, schema string) ([]*models.Table, error) {
		result := load(ctx, guicontext.SchemaRailTabTables, schema)
		items := make([]*models.Table, len(result.names))
		for i, name := range result.names {
			items[i] = &models.Table{Schema: schema, Name: name}
		}
		return items, result.err
	}
	g, d, _, _ := reviewSchemaGui(t, sess)
	t.Cleanup(unblock)
	sess.stage.Store(1)
	g.Registry().Schemas.SetCursor(0)
	g.Registry().Tables.SetItems([]any{
		&models.Table{Schema: "a", Name: "a"},
		&models.Table{Schema: "a", Name: "b"},
		&models.Table{Schema: "a", Name: "c"},
	})
	return g, d, calls, unblock
}

func reviewRefreshStart(t *testing.T, g *orchestrator.Gui, d *queueingDriver, tab int, unblock func()) {
	t.Helper()
	g.Registry().SchemaRail.SetActiveTab(tab)
	cmd, ok := g.CommandRegistry().Get(commands.SchemaRailRefresh)
	require.True(t, ok)
	done := make(chan error, 1)
	go func() { done <- cmd.Handler(commands.ExecCtx{}) }()
	select {
	case err := <-done:
		require.NoError(t, err)
		reviewInspectorRun(t, reviewInspectorCallbacks(d))
	case <-time.After(time.Second):
		unblock()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("refresh handler did not settle after fixture release")
		}
		t.Fatal("refresh blocked MainLoop on metadata")
	}
}

func reviewRefreshNext(t *testing.T, calls <-chan reviewRefreshLoad, tab int) reviewRefreshLoad {
	t.Helper()
	select {
	case call := <-calls:
		require.Equal(t, tab, call.tab)
		return call
	case <-time.After(3 * time.Second):
		t.Fatal("refresh did not reach metadata worker")
		return reviewRefreshLoad{}
	}
}

func reviewRefreshRail(g *orchestrator.Gui, tab int) *guicontext.SideListContext {
	if tab == guicontext.SchemaRailTabSchemas {
		return &g.Registry().Schemas.SideListContext
	}
	return &g.Registry().Tables.SideListContext
}

func TestReview_schema_refresh_delayed(t *testing.T) {
	for _, tab := range []int{guicontext.SchemaRailTabSchemas, guicontext.SchemaRailTabTables} {
		for _, scenario := range []string{"reordered", "removed", "empty", "error", "canceled_error"} {
			t.Run(fmt.Sprintf("%d/%s", tab, scenario), func(t *testing.T) {
				g, d, calls, unblock := reviewRefreshFixture(t)
				rail := reviewRefreshRail(g, tab)
				require.Len(t, rail.Items(), 3)
				before := append([]any(nil), rail.Items()...)
				reviewRefreshStart(t, g, d, tab, unblock)
				call := reviewRefreshNext(t, calls, tab)
				require.Positive(t, g.BusyCount())
				rail.SetCursor(1)
				result := reviewRefreshReply{names: []string{"c", "a", "b"}}
				switch scenario {
				case "removed":
					result.names = []string{"a", "b"}
				case "empty":
					result.names = nil
				case "error":
					result.err = errors.New("synthetic metadata failure")
				case "canceled_error":
					result.err = context.Canceled
				}
				call.reply <- result
				g.WaitForWorkersForTest()
				pending := reviewInspectorCallbacks(d)
				require.Equal(t, before, rail.Items(), "worker may not publish rows")
				require.Equal(t, 1, rail.Cursor(), "worker may not move selection")
				rail.SetCursor(2)
				reviewInspectorRun(t, pending)
				require.Zero(t, g.BusyCount())
				require.Equal(t, tab, g.Registry().SchemaRail.ActiveTab())
				require.Equal(t, types.SCHEMA_RAIL, g.ContextTree().Current().GetKey())
				switch scenario {
				case "error", "canceled_error":
					require.Equal(t, before, rail.Items())
					require.Equal(t, 2, rail.Cursor())
				case "empty":
					require.Empty(t, rail.Items())
					require.Zero(t, rail.Cursor())
				case "removed":
					require.Len(t, rail.Items(), 2)
					require.Equal(t, 1, rail.Cursor())
				default:
					require.Equal(t, 0, rail.Cursor(), "preserve selection at delivery, not request start")
					if tab == guicontext.SchemaRailTabSchemas {
						require.Equal(t, "c", rail.SelectedItem().(models.Schema).Name)
					} else {
						require.Equal(t, "c", rail.SelectedItem().(*models.Table).Name)
					}
				}
			})
		}
	}
}

func TestReview_schema_refresh_queued_publication(t *testing.T) {
	for _, tab := range []int{guicontext.SchemaRailTabSchemas, guicontext.SchemaRailTabTables} {
		for _, scenario := range []string{"newer_before", "newer_after", "connection_generation", "schema_changed", "close"} {
			t.Run(fmt.Sprintf("%d/%s", tab, scenario), func(t *testing.T) {
				g, d, calls, unblock := reviewRefreshFixture(t)
				rail := reviewRefreshRail(g, tab)
				before := append([]any(nil), rail.Items()...)
				reviewRefreshStart(t, g, d, tab, unblock)
				call := reviewRefreshNext(t, calls, tab)
				call.reply <- reviewRefreshReply{names: []string{"old"}}
				g.WaitForWorkersForTest()
				old := reviewInspectorCallbacks(d)
				require.NotEmpty(t, old)
				switch scenario {
				case "newer_before", "newer_after":
					reviewRefreshStart(t, g, d, tab, unblock)
					call = reviewRefreshNext(t, calls, tab)
					call.reply <- reviewRefreshReply{names: []string{"c", "a", "b"}}
					g.WaitForWorkersForTest()
					fresh := reviewInspectorCallbacks(d)
					if scenario == "newer_before" {
						reviewInspectorRun(t, old)
						require.Equal(t, before, rail.Items())
					}
					reviewInspectorRun(t, fresh)
					before = append([]any(nil), rail.Items()...)
				case "connection_generation":
					g.BumpConnectGenForTest()
				case "schema_changed":
					g.Registry().Schemas.SetCursor(1)
					if tab == guicontext.SchemaRailTabSchemas {
						// Schema refresh is independent of the currently highlighted schema.
						before = []any{models.Schema{Name: "old"}}
					}
				case "close":
					require.NoError(t, g.Close())
				}
				reviewInspectorRun(t, old)
				require.Equal(t, before, rail.Items())
			})
		}
	}
}

func TestReview_schema_refresh_cancel_loading(t *testing.T) {
	for _, tab := range []int{guicontext.SchemaRailTabSchemas, guicontext.SchemaRailTabTables} {
		for _, scenario := range []string{"superseded", "activation", "close"} {
			t.Run(fmt.Sprintf("%d/%s", tab, scenario), func(t *testing.T) {
				g, d, calls, unblock := reviewRefreshFixture(t)
				reviewRefreshStart(t, g, d, tab, unblock)
				old := reviewRefreshNext(t, calls, tab)
				switch scenario {
				case "close":
					require.NoError(t, g.Close())
				case "activation":
					reviewSchemaActivate(t, g, "b")
				default:
					reviewRefreshStart(t, g, d, tab, unblock)
				}
				select {
				case <-old.ctx.Done():
				case <-time.After(time.Second):
					t.Fatal("supersession/activation/shutdown did not cancel metadata load")
				}
				if scenario == "activation" {
					call := reviewRefreshNext(t, calls, guicontext.SchemaRailTabSchemas)
					call.reply <- reviewRefreshReply{names: []string{"a", "b", "c"}}
					call = reviewRefreshNext(t, calls, guicontext.SchemaRailTabTables)
					require.Equal(t, "b", call.schema)
					call.reply <- reviewRefreshReply{names: []string{"fresh"}}
					// Activation also warms completion metadata through this queue.
					warm := reviewRefreshNext(t, calls, guicontext.SchemaRailTabTables)
					warm.reply <- reviewRefreshReply{names: []string{"fresh"}}
				} else if scenario != "close" {
					call := reviewRefreshNext(t, calls, tab)
					call.reply <- reviewRefreshReply{names: []string{"fresh"}}
				}
				g.WaitForWorkersForTest()
				reviewInspectorRun(t, reviewInspectorCallbacks(d))
				require.Zero(t, g.BusyCount())
			})
		}
	}
}

func TestReview_schema_refresh_busy_queue(t *testing.T) {
	for _, tab := range []int{guicontext.SchemaRailTabSchemas, guicontext.SchemaRailTabTables} {
		t.Run(fmt.Sprint(tab), func(t *testing.T) {
			g, d, calls, unblock := reviewRefreshFixture(t)
			done := make(chan error, 1)
			go func() {
				_, err := g.MetadataHelperForTest().LoadTables(context.Background(), "unrelated")
				done <- err
			}()
			blocker := reviewRefreshNext(t, calls, guicontext.SchemaRailTabTables)
			require.Equal(t, "unrelated", blocker.schema)
			reviewRefreshStart(t, g, d, tab, unblock)
			reviewRefreshStart(t, g, d, tab, unblock)
			rail := reviewRefreshRail(g, tab)
			rail.SetCursor(2)
			repainted := false
			g.OnUIThread(func() error { repainted = true; return nil })
			reviewInspectorRun(t, reviewInspectorCallbacks(d))
			require.True(t, repainted, "busy metadata queue must not park the UI lane")
			blocker.reply <- reviewRefreshReply{}
			require.NoError(t, <-done)
			latest := reviewRefreshNext(t, calls, tab)
			latest.reply <- reviewRefreshReply{names: []string{"c", "a", "b"}}
			g.WaitForWorkersForTest()
			reviewInspectorRun(t, reviewInspectorCallbacks(d))
			require.Zero(t, rail.Cursor())
			require.Zero(t, g.BusyCount())
			select {
			case call := <-calls:
				t.Fatalf("superseded refresh reached driver: tab=%d schema=%q", call.tab, call.schema)
			default:
			}
		})
	}
}

func TestReview_schema_refresh_supersedes_activation_publication(t *testing.T) {
	for _, tab := range []int{guicontext.SchemaRailTabSchemas, guicontext.SchemaRailTabTables} {
		t.Run(fmt.Sprint(tab), func(t *testing.T) {
			g, d, calls, unblock := reviewRefreshFixture(t)
			reviewSchemaActivate(t, g, "b")
			call := reviewRefreshNext(t, calls, guicontext.SchemaRailTabSchemas)
			call.reply <- reviewRefreshReply{names: []string{"a", "b", "c"}}
			call = reviewRefreshNext(t, calls, guicontext.SchemaRailTabTables)
			call.reply <- reviewRefreshReply{names: []string{"old_activation"}}
			warm := reviewRefreshNext(t, calls, guicontext.SchemaRailTabTables)
			warm.reply <- reviewRefreshReply{names: []string{"old_activation"}}
			g.WaitForWorkersForTest()
			old := reviewInspectorCallbacks(d)
			reviewRefreshStart(t, g, d, tab, unblock)
			call = reviewRefreshNext(t, calls, tab)
			call.reply <- reviewRefreshReply{names: []string{"fresh"}}
			g.WaitForWorkersForTest()
			reviewInspectorRun(t, reviewInspectorCallbacks(d))
			rail := reviewRefreshRail(g, tab)
			before := append([]any(nil), rail.Items()...)
			reviewInspectorRun(t, old)
			require.Equal(t, before, rail.Items(), "older activation may not replace newer refresh data")
		})
	}
}

func TestReview_schema_refresh_connection_publication(t *testing.T) {
	for _, tab := range []int{guicontext.SchemaRailTabSchemas, guicontext.SchemaRailTabTables} {
		for _, refreshDuringConnect := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/pending=%v", tab, refreshDuringConnect), func(t *testing.T) {
				g, d, _, _ := reviewSchemaGui(t, &reviewSchemaSession{})
				g.Registry().Schemas.SetCursor(0)
				var refreshCallbacks []func() error
				refresh := func() {
					reviewRefreshStart(t, g, d, tab, func() {})
					g.WaitForWorkersForTest()
					refreshCallbacks = reviewInspectorCallbacks(d)
				}
				if !refreshDuringConnect {
					refresh()
				}
				oldSQL := g.ActiveSQLSessionForTest()
				oldSQL.SetDisconnected(true)
				cmd, ok := g.CommandRegistry().Get(commands.Reconnect)
				require.True(t, ok)
				require.NoError(t, cmd.Handler(commands.ExecCtx{}))
				g.WaitForWorkersForTest()
				connectionCallbacks := reviewInspectorCallbacks(d)
				require.Same(t, oldSQL, g.ActiveSQLSessionForTest())
				if refreshDuringConnect {
					refresh()
				}
				reviewInspectorRun(t, connectionCallbacks)
				g.WaitForWorkersForTest()
				d.drainPending()
				require.NotSame(t, oldSQL, g.ActiveSQLSessionForTest())
				rail := reviewRefreshRail(g, tab)
				var fresh []any
				if tab == guicontext.SchemaRailTabSchemas {
					fresh = []any{models.Schema{Name: "new_connection"}}
				} else {
					fresh = []any{&models.Table{Schema: "a", Name: "new_connection"}}
				}
				rail.SetItems(fresh)
				require.NoError(t, g.ContextTree().Push(g.Registry().QueryRail))
				reviewInspectorRun(t, refreshCallbacks)
				require.Equal(t, fresh, rail.Items())
				require.Equal(t, types.QUERY_RAIL, g.ContextTree().Current().GetKey())
			})
		}
	}
}

func TestReview_schema_refresh_table_a_b_a(t *testing.T) {
	g, d, calls, unblock := reviewRefreshFixture(t)
	var old []func() error
	for i, schemaCursor := range []int{0, 1, 0} {
		g.Registry().Schemas.SetCursor(schemaCursor)
		reviewRefreshStart(t, g, d, guicontext.SchemaRailTabTables, unblock)
		call := reviewRefreshNext(t, calls, guicontext.SchemaRailTabTables)
		call.reply <- reviewRefreshReply{names: []string{fmt.Sprintf("revision%d", i)}}
		g.WaitForWorkersForTest()
		pending := reviewInspectorCallbacks(d)
		if i < 2 {
			old = append(old, pending...)
		} else {
			reviewInspectorRun(t, pending)
		}
	}
	reviewInspectorRun(t, old)
	selected := g.Registry().Tables.SelectedItem().(*models.Table)
	require.Equal(t, "a", selected.Schema)
	require.Equal(t, "revision2", selected.Name)
}

func TestReview_schema_refresh_worker_admission(t *testing.T) {
	g, d, calls, _ := reviewRefreshFixture(t)
	done := make(chan error, 1)
	go func() { done <- g.RefreshHelperForTest().RefreshSchemas(context.Background()) }()
	require.NoError(t, <-done)
	require.Zero(t, g.BusyCount(), "worker-origin refresh must await MainLoop admission before reading UI state")
	pending := reviewInspectorCallbacks(d)
	require.NotEmpty(t, pending)
	// A retired admission must not capture the replacement's UI state/session.
	g.BumpConnectGenForTest()
	reviewInspectorRun(t, pending)
	g.WaitForWorkersForTest()
	select {
	case <-calls:
		t.Fatal("retired admission reached the metadata driver")
	default:
	}
}

func TestReview_schema_refresh_live_reconnect_publication(t *testing.T) {
	g, d, _, _ := reviewSchemaGui(t, &reviewSchemaSession{})
	cmd, ok := g.CommandRegistry().Get(commands.Reconnect)
	require.True(t, ok)
	for range 10 {
		g.ActiveSQLSessionForTest().SetDisconnected(true)
		require.NoError(t, cmd.Handler(commands.ExecCtx{}))
		deadline := time.Now().Add(3 * time.Second)
		for {
			// Deliver connection publication while the reconnect worker may
			// still be scheduling its follow-up refresh, as MainLoop does.
			reviewInspectorRun(t, reviewInspectorCallbacks(d))
			if g.BusyCount() == 0 {
				break
			}
			require.True(t, time.Now().Before(deadline), "reconnect did not settle")
			runtime.Gosched()
		}
		g.WaitForWorkersForTest()
		d.drainPending()
		g.WaitForWorkersForTest()
		d.drainPending()
	}
}
