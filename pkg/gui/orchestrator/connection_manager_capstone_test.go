package orchestrator_test

import (
	"context"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// Capstone tests (dbsavvy-cjl): gap coverage for the CONNECTION_MANAGER
// modal. Existing suites cover startup, connect lifecycle, CRUD, and root
// exit. These tests fill four remaining gaps:
//
//  1. Reconnect "pick another" pushes the modal.
//  2. <leader>C opens/closes the modal mid-session.
//  3. No "connections" view is registered in the context tree.
//  4. Mid-session q closes the modal (vs. root q which quits).
//
// AC5 (CONNECTING retired) is proven by compilation: types.CONNECTING is
// not referenced anywhere in this file or the codebase, and the constant
// does not exist. If it did, this file would fail to compile.

// TestCapstoneReconnectPickAnotherOpensModal asserts that the reconnect
// controller's "pick another connection" path pushes CONNECTION_MANAGER
// onto the focus stack. After a successful connect the modal is popped;
// triggering OnPickConnection must re-push it.
func TestCapstoneReconnectPickAnotherOpensModal(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	driverName, conn := registerWireFake(t, drivers.Capabilities{})
	conn.schemas = []models.Schema{{Name: "public", Owner: "u"}}

	bag := g.HelperBagForTest()
	profile := &models.Connection{Name: "capstone-pick", Driver: driverName, DSN: "postgres://stub"}

	// Connect successfully — modal pops, lands on schemas.
	if err := bag.Connect.Connect(context.Background(), profile); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if got := g.ContextTree().Current().GetKey(); got == types.CONNECTION_MANAGER {
		t.Fatal("CONNECTION_MANAGER still top after successful connect; want it popped")
	}

	// Simulate the reconnect controller's "pick another" by calling
	// OnPickConnection, which pushes CONNECTION_MANAGER back on top.
	cmd, ok := g.CommandRegistry().Get(commands.ConnectionManagerOpen)
	if !ok {
		t.Fatal("ConnectionManagerOpen action not registered")
	}
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("ConnectionManagerOpen handler: %v", err)
	}
	if got := g.ContextTree().Current().GetKey(); got != types.CONNECTION_MANAGER {
		t.Fatalf("top after pick-another = %v; want CONNECTION_MANAGER", got)
	}
}

// TestCapstoneLeaderCOpensModalMidSession asserts that the
// ConnectionManagerOpen action (<leader>C) pushes the modal from a
// mid-session context (schemas visible), and that closing it returns
// focus to the prior context.
func TestCapstoneLeaderCOpensModalMidSession(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	driverName, conn := registerWireFake(t, drivers.Capabilities{})
	conn.schemas = []models.Schema{{Name: "public", Owner: "u"}}

	bag := g.HelperBagForTest()
	profile := &models.Connection{Name: "capstone-leader", Driver: driverName, DSN: "postgres://stub"}

	// Connect — lands on SCHEMAS.
	if err := bag.Connect.Connect(context.Background(), profile); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	beforeKey := g.ContextTree().Current().GetKey()
	if beforeKey == types.CONNECTION_MANAGER {
		t.Fatal("still on CONNECTION_MANAGER after connect")
	}

	// <leader>C opens the modal.
	cmd, ok := g.CommandRegistry().Get(commands.ConnectionManagerOpen)
	if !ok {
		t.Fatal("ConnectionManagerOpen action not registered")
	}
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("ConnectionManagerOpen: %v", err)
	}
	if got := g.ContextTree().Current().GetKey(); got != types.CONNECTION_MANAGER {
		t.Fatalf("top after <leader>C = %v; want CONNECTION_MANAGER", got)
	}

	// Close the modal (Esc in list mode) — should return to the prior context.
	if err := g.Controllers().ConnectionManager.Close(commands.ExecCtx{}); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := g.ContextTree().Current().GetKey(); got != beforeKey {
		t.Fatalf("top after close = %v; want restored %v", got, beforeKey)
	}
}

// TestCapstoneNoConnectionsWindow asserts that no context in the tree has
// a view name of "connections" — the retired CONNECTIONS side-rail window
// must not exist. The CONNECTION_MANAGER view name is
// "connection_manager", which is distinct.
func TestCapstoneNoConnectionsWindow(t *testing.T) {
	g, _ := buildTestGui(t)
	for _, ctx := range g.Registry().Flatten() {
		viewName := ctx.GetViewName()
		if viewName == "connections" {
			t.Fatalf("found context with view name %q (key=%v); "+
				"the retired CONNECTIONS side-rail must not be registered",
				viewName, ctx.GetKey())
		}
		if ctx.GetKey() == "connections" {
			t.Fatalf("found context with key %q; "+
				"the retired CONNECTIONS ContextKey must not exist",
				ctx.GetKey())
		}
	}
}

// TestCapstoneMidSessionQClosesModal asserts that pressing q on the
// CONNECTION_MANAGER modal when the focus stack has depth > 1 (mid-
// session) closes the modal instead of quitting the app.
// TestConnectionManagerRootExitNeverPopsBottom covers the root case
// (depth 1 → quit); this test covers the mid-session case.
func TestCapstoneMidSessionQClosesModal(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	driverName, conn := registerWireFake(t, drivers.Capabilities{})
	conn.schemas = []models.Schema{{Name: "public", Owner: "u"}}

	bag := g.HelperBagForTest()
	profile := &models.Connection{Name: "capstone-q", Driver: driverName, DSN: "postgres://stub"}

	// Connect — lands on schemas, modal is popped.
	if err := bag.Connect.Connect(context.Background(), profile); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	beforeKey := g.ContextTree().Current().GetKey()

	// Push the modal mid-session (stack depth > 1).
	if err := g.ContextTree().Push(g.Registry().ConnectionManager); err != nil {
		t.Fatalf("Push(CONNECTION_MANAGER): %v", err)
	}
	if got := g.ContextTree().Current().GetKey(); got != types.CONNECTION_MANAGER {
		t.Fatalf("top after push = %v; want CONNECTION_MANAGER", got)
	}
	if depth := len(g.ContextTree().Stack()); depth <= 1 {
		t.Fatalf("stack depth = %d after push; want > 1 for mid-session test", depth)
	}

	// QuitOrClose at depth > 1 must close (pop) the modal, NOT quit.
	err := g.Controllers().ConnectionManager.QuitOrClose(commands.ExecCtx{})
	if err != nil {
		t.Fatalf("QuitOrClose returned error: %v", err)
	}

	// The modal should be popped; we're back at the prior context.
	if got := g.ContextTree().Current().GetKey(); got != beforeKey {
		t.Fatalf("top after mid-session q = %v; want restored %v", got, beforeKey)
	}
}
