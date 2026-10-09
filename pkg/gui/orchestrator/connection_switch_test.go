package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/editor"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/stretchr/testify/require"
)

// TestConnectionManagerModalSwitchConnection reproduces the bug: while
// already connected, re-opening the CONNECTION_MANAGER modal (<leader>C) and
// selecting a DIFFERENT profile must tear down the current connection and
// connect to the new one. Before the fix, connectWithGen called
// ConnectHelper.Connect without a preceding Disconnect, so the still-live
// session tripped the "data: already connected" guard and the switch silently
// failed (activeConnID stayed on the old profile).
func TestConnectionManagerModalSwitchConnection(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	caps := drivers.Capabilities{}
	driverName, conn := registerWireFake(t, caps)
	conn.schemas = []models.Schema{{Name: "public"}}

	reg := g.Registry()
	modal := reg.ConnectionManager
	tree := g.ContextTree()

	profileA := &models.Connection{Name: "pg-a", Driver: driverName, DSN: "postgres://a"}
	profileB := &models.Connection{Name: "pg-b", Driver: driverName, DSN: "postgres://b"}

	// First connect: select pg-a from the modal.
	if err := tree.Push(modal); err != nil {
		t.Fatalf("push modal (1st): %v", err)
	}
	modal.SetItems([]any{profileA, profileB})
	modal.SetCursor(0)
	if err := g.Controllers().ConnectionManager.Confirm(commands.ExecCtx{}); err != nil {
		t.Fatalf("Confirm pg-a: %v", err)
	}
	g.WaitForWorkersForTest()
	if got := g.ActiveConnIDForTest(); got != "pg-a" {
		t.Fatalf("activeConnID after 1st connect = %q, want pg-a", got)
	}

	// Switch: <leader>C re-opens the modal mid-session; pick the OTHER profile.
	if err := tree.Push(modal); err != nil {
		t.Fatalf("push modal (2nd): %v", err)
	}
	modal.SetItems([]any{profileA, profileB})
	modal.SetCursor(1)
	if err := g.Controllers().ConnectionManager.Confirm(commands.ExecCtx{}); err != nil {
		t.Fatalf("Confirm pg-b: %v", err)
	}
	g.WaitForWorkersForTest()

	if got := g.ActiveConnIDForTest(); got != "pg-b" {
		t.Fatalf("activeConnID after switch = %q, want pg-b (connection switch failed)", got)
	}
	// The modal must be popped on a successful switch, not left showing an error.
	if top := tree.Current(); top != nil && top.GetKey() == types.CONNECTION_MANAGER {
		t.Fatalf("modal still top after switch; want it popped")
	}
}

func TestReview_schema_reset_ProfileCompletion(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)
	nameA, connA := registerWireFake(t, drivers.Capabilities{})
	nameB, connB := registerWireFake(t, drivers.Capabilities{})
	for _, tc := range []struct {
		conn        *wireFakeConn
		typ, target string
	}{
		{connA, "integer", "accounts_a"},
		{connB, "text", "accounts_b"},
	} {
		tc.conn.schemas = []models.Schema{{Name: "public"}}
		tc.conn.columns = []models.Column{{Name: "id", DataType: tc.typ}}
		tc.conn.outboundFKs = []models.ForeignKey{{Schema: "public", Table: "users", Columns: []string{"id"}, RefSchema: "public", RefTable: tc.target, RefColumns: []string{"id"}}}
	}
	profiles := []any{&models.Connection{Name: "a", Driver: nameA}, &models.Connection{Name: "b", Driver: nameB}}
	modal := g.Registry().ConnectionManager
	engine := g.Controllers().VimEditor.CompletionEngineForTest()
	sql := "SELECT public.users."
	buf := &editor.Buffer{Lines: []editor.Line{{Runes: []rune(sql)}}}
	pos := editor.Position{Col: len([]rune(sql))}
	for _, step := range []struct {
		idx         int
		typ, target string
	}{
		{0, "integer", "accounts_a"},
		{0, "integer", "accounts_a"},
		{1, "text", "accounts_b"},
		{0, "integer", "accounts_a"},
	} {
		// Each step selects from the list, rather than retrying a prior error.
		modal.SetMode(guicontext.ModeList)
		require.NoError(t, g.ContextTree().Push(modal))
		modal.SetItems(profiles)
		modal.SetCursor(step.idx)
		require.NoError(t, g.Controllers().ConnectionManager.Confirm(commands.ExecCtx{}))
		g.WaitForWorkersForTest()
		require.Equal(t, profiles[step.idx].(*models.Connection).Name, g.ActiveConnIDForTest())
		engine.Trigger(context.Background(), buf, pos)
		g.WaitForWorkersForTest()
		suggestions := engine.Trigger(context.Background(), buf, pos)
		found := false
		for _, suggestion := range suggestions {
			if suggestion.Text == "id" && suggestion.Source == editor.SchemaSourceName {
				found = true
				require.Equal(t, step.typ, suggestion.Detail)
				require.Equal(t, "public."+step.target+".id", suggestion.FKRef)
			}
		}
		require.True(t, found, "same-named table must expose current database metadata")
	}
}

type reviewSwitchTablesConn struct {
	wireFakeConn
	tables   []*models.Table
	tableErr error
	queries  chan models.Query
}

func (c *reviewSwitchTablesConn) AcquireSession(ctx context.Context) (drivers.Session, error) {
	inner, err := c.wireFakeConn.AcquireSession(ctx)
	if err != nil {
		return nil, err
	}
	return &reviewSwitchTablesSession{wireFakeSession: inner.(*wireFakeSession), conn: c}, nil
}

type reviewSwitchTablesSession struct {
	*wireFakeSession
	conn *reviewSwitchTablesConn
}

func (s *reviewSwitchTablesSession) ListTables(context.Context, string) ([]*models.Table, error) {
	return s.conn.tables, s.conn.tableErr
}

func (s *reviewSwitchTablesSession) Stream(_ context.Context, q models.Query) (drivers.RowStream, error) {
	s.conn.queries <- q
	return eofRowStream{qid: models.QueryID{SessionID: s.ID(), Nonce: 1}}, nil
}

type reviewSwitchTablesDriver struct {
	wireFakeDriver
	connection *reviewSwitchTablesConn
}

func (d *reviewSwitchTablesDriver) Open(ctx context.Context, profile drivers.ConnectionProfile, reporter drivers.ProgressReporter) (drivers.Connection, error) {
	if _, err := d.wireFakeDriver.Open(ctx, profile, reporter); err != nil {
		return nil, err
	}
	return d.connection, nil
}

func reviewRegisterSwitchTables(t *testing.T, conn *reviewSwitchTablesConn) string {
	t.Helper()
	name := fmt.Sprintf("review-switch-tables-%d", uniqueDriverNameCounter.Add(1))
	drivers.Register(name, func(context.Context) (drivers.Driver, error) {
		return &reviewSwitchTablesDriver{wireFakeDriver: wireFakeDriver{conn: &conn.wireFakeConn}, connection: conn}, nil
	})
	return name
}

func TestReview_connection_switch_table_metadata(t *testing.T) {
	for _, scenario := range []string{"no_saved_schema", "missing_saved_schema", "schema_error", "table_error", "empty_tables", "same_named_servers", "restored_rows", "different_schema", "failed_switch"} {
		t.Run(scenario, func(t *testing.T) {
			d := newQueueingDriver()
			g, store := buildPickerGuiWithDriver(t, func() []models.Connection { return nil }, d)
			d.drainPending()
			old := &models.Table{Schema: "public", Name: "shared", Owner: "server_a"}
			a := &reviewSwitchTablesConn{
				wireFakeConn: wireFakeConn{schemas: []models.Schema{{Name: "public"}}},
				tables:       []*models.Table{{Schema: "public", Name: "first"}, old},
				queries:      make(chan models.Query, 1),
			}
			var retired atomic.Bool
			a.closeHook = func() { retired.Store(true) }
			schema := "public"
			if scenario == "different_schema" {
				schema = "other"
			}
			fresh := &models.Table{Schema: schema, Name: "shared", Owner: "server_b"}
			b := &reviewSwitchTablesConn{
				wireFakeConn: wireFakeConn{schemas: []models.Schema{{Name: "alpha"}, {Name: schema}}},
				tables:       []*models.Table{fresh, {Schema: schema, Name: "first"}},
				queries:      make(chan models.Query, 1),
			}
			wantTableCursor := 0
			if scenario == "restored_rows" {
				b.tables = []*models.Table{{Schema: schema, Name: "first"}, {Schema: schema, Name: "second"}, fresh}
				wantTableCursor = 2
			}
			profileA := &models.Connection{Name: "server_a", Driver: reviewRegisterSwitchTables(t, a)}
			profileB := &models.Connection{Name: "server_b", Driver: reviewRegisterSwitchTables(t, b)}
			store.SetLastSchemaName(profileA.Name, "public")
			store.SetLastTableName(profileA.Name, "shared")
			if scenario != "no_saved_schema" {
				store.SetLastSchemaName(profileB.Name, schema)
				store.SetLastTableName(profileB.Name, "shared")
			}
			switch scenario {
			case "missing_saved_schema":
				store.SetLastSchemaName(profileB.Name, "gone")
			case "schema_error":
				b.schemasErr = errors.New("synthetic schema load failure")
			case "table_error":
				b.tableErr = errors.New("synthetic table load failure")
			case "empty_tables":
				b.tables = nil
			case "failed_switch":
				b.openErr = errors.New("synthetic dial failure")
			}
			modal := g.Registry().ConnectionManager
			connect := func(index int) {
				modal.SetMode(guicontext.ModeList)
				require.NoError(t, g.ContextTree().Push(modal))
				modal.SetItems([]any{profileA, profileB})
				modal.SetCursor(index)
				require.NoError(t, g.Controllers().ConnectionManager.Confirm(commands.ExecCtx{}))
				g.WaitForWorkersForTest()
				reviewInspectorRun(t, reviewInspectorCallbacks(d))
				g.WaitForWorkersForTest()
				reviewInspectorRun(t, reviewInspectorCallbacks(d))
			}
			connect(0)
			rail := g.Registry().Tables
			require.Same(t, old, rail.SelectedItem())
			require.Equal(t, 1, rail.Cursor())
			rail.SetSearch("shared")
			before := append([]any(nil), rail.Items()...)
			connect(1)
			require.True(t, retired.Load(), "switch retires A even when B fails")
			if scenario == "failed_switch" {
				require.True(t, modal.ConnectingState().IsError())
				require.Equal(t, before, rail.Items())
				require.Equal(t, 1, rail.Cursor())
				require.True(t, rail.SearchActive())
				require.NotEqual(t, profileB.Name, g.ActiveConnIDForTest())
				return
			}
			require.Equal(t, profileB.Name, g.ActiveConnIDForTest())
			populated := scenario == "same_named_servers" || scenario == "restored_rows" || scenario == "different_schema"
			if populated {
				require.Len(t, rail.Items(), len(b.tables))
				for i, table := range b.tables {
					require.Same(t, table, rail.Items()[i])
				}
				require.Same(t, fresh, rail.SelectedItem(), "same name is not the same server's metadata object")
				require.NotSame(t, old, rail.SelectedItem())
				require.Equal(t, 1, g.Registry().Schemas.Cursor())
				require.Equal(t, wantTableCursor, rail.Cursor(), "restore B's saved selection, not A's numeric cursor")
				require.Equal(t, guicontext.SchemaRailTabTables, g.Registry().SchemaRail.ActiveTab())
			} else {
				require.Empty(t, rail.Items())
				require.Nil(t, rail.SelectedItem())
				require.Zero(t, rail.Cursor())
			}
			require.False(t, rail.SearchActive(), "outgoing metadata search must not survive replacement")
			lastTable := store.LastTableNameSnapshot(profileB.Name)
			g.Registry().SchemaRail.SetActiveTab(guicontext.SchemaRailTabTables)
			require.NoError(t, g.ContextTree().Push(g.Registry().SchemaRail))
			confirm, ok := g.CommandRegistry().Get(commands.SchemaRailConfirm)
			require.True(t, ok)
			require.NoError(t, confirm.Handler(commands.ExecCtx{}))
			if populated {
				select {
				case q := <-b.queries:
					require.Equal(t, `SELECT * FROM "`+schema+`"."shared" LIMIT 100`, q.SQL)
				case <-time.After(3 * time.Second):
					t.Fatal("table activation did not reach B's session")
				}
				deadline := time.Now().Add(3 * time.Second)
				for g.ResultTabsHelper().Active() == nil {
					reviewInspectorRun(t, reviewInspectorCallbacks(d))
					require.True(t, time.Now().Before(deadline), "preview publication did not arrive")
					time.Sleep(time.Millisecond)
				}
			} else {
				require.Equal(t, lastTable, store.LastTableNameSnapshot(profileB.Name), "empty table confirmation must not activate outgoing metadata")
				select {
				case <-b.queries:
					t.Fatal("empty table rail started a query")
				default:
				}
			}
			select {
			case <-a.queries:
				t.Fatal("table activation reached retired server A")
			default:
			}
			require.NoError(t, g.Close())
		})
	}
}
