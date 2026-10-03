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
	stage    atomic.Int32
	setError bool
	tableErr bool
	block    bool
	entered  chan struct{}
	release  chan struct{}
	canceled chan struct{}
	once     sync.Once
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

func (s *reviewSchemaSession) ListSchemas(context.Context, string) ([]models.Schema, error) {
	return []models.Schema{{Name: "a"}, {Name: "b"}}, nil
}

func (s *reviewSchemaSession) ListTables(_ context.Context, schema string) ([]*models.Table, error) {
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
