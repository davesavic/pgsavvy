package orchestrator_test

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// TestConnectionManagerModalSwitchConnection reproduces dbsavvy-k70h: while
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
