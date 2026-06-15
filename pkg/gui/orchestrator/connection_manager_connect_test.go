package orchestrator_test

import (
	"errors"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// TestConnectionManagerModalConnectSuccessPopsModal asserts the in-modal
// connect lifecycle: pushing the modal + <CR> on a row dials
// via connectInvoker (no standalone CONNECTING push), and on success the modal
// is popped and the user lands in the restored schemas/tables navigation.
func TestConnectionManagerModalConnectSuccessPopsModal(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	caps := drivers.Capabilities{}
	driverName, conn := registerWireFake(t, caps)
	conn.schemas = []models.Schema{{Name: "public"}}

	reg := g.Registry()
	modal := reg.ConnectionManager
	tree := g.ContextTree()

	profile := &models.Connection{Name: "modal-pg", Driver: driverName, DSN: "postgres://stub"}
	if err := tree.Push(modal); err != nil {
		t.Fatalf("push modal: %v", err)
	}
	// Seed rows AFTER push: HandleFocus on push runs onShow which reloads from
	// the (nil) provider, so the test seeds the row slice once the modal is top.
	modal.SetItems([]any{profile})
	if top := tree.Current(); top == nil || top.GetKey() != types.CONNECTION_MANAGER {
		t.Fatalf("modal not top after push: %v", top)
	}

	// <CR> on the selected row → in-modal connect.
	if err := g.Controllers().ConnectionManager.Confirm(commands.ExecCtx{}); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	g.WaitForWorkersForTest()

	if got := g.ActiveConnIDForTest(); got != "modal-pg" {
		t.Fatalf("activeConnID = %q, want modal-pg", got)
	}
	// The modal must be popped — the user lands in schemas/tables, not the modal.
	if top := tree.Current(); top != nil && top.GetKey() == types.CONNECTION_MANAGER {
		t.Fatalf("modal still top after successful connect; want it popped")
	}
}

// TestConnectionManagerModalConnectErrorStaysInModal asserts a dial failure
// keeps the modal top and routes the error into the modal's own connecting
// state — NOT the standalone CONNECTING screen — and
// does NOT set the active connection.
func TestConnectionManagerModalConnectErrorStaysInModal(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	caps := drivers.Capabilities{}
	driverName, conn := registerWireFake(t, caps)
	conn.openErr = errors.New("connection refused")

	reg := g.Registry()
	modal := reg.ConnectionManager
	tree := g.ContextTree()

	profile := &models.Connection{Name: "bad-pg", Driver: driverName, DSN: "postgres://stub"}
	if err := tree.Push(modal); err != nil {
		t.Fatalf("push modal: %v", err)
	}
	modal.SetItems([]any{profile})

	if err := g.Controllers().ConnectionManager.Confirm(commands.ExecCtx{}); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	g.WaitForWorkersForTest()

	if got := g.ActiveConnIDForTest(); got != "" {
		t.Fatalf("activeConnID = %q after failed modal connect; want empty", got)
	}
	// The modal stays top so it can render the error + [r]/[Esc] body.
	if top := tree.Current(); top == nil || top.GetKey() != types.CONNECTION_MANAGER {
		t.Fatalf("modal not top after failed connect: %v", top)
	}
	if modal.Mode() != guicontext.ModeConnecting {
		t.Fatalf("modal mode = %v after failed connect, want ModeConnecting", modal.Mode())
	}
}
