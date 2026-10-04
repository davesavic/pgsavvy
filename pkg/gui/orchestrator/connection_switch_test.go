package orchestrator_test

import (
	"context"
	"testing"

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
