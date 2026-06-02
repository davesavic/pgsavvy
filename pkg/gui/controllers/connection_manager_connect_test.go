package controllers_test

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// newModalCtx builds a live ConnectionManagerContext for controller wiring
// tests (no driver — render is not exercised here).
func newModalCtx() *guicontext.ConnectionManagerContext {
	base := guicontext.NewBaseContext(guicontext.BaseContextOpts{
		Key:      types.CONNECTION_MANAGER,
		ViewName: string(types.CONNECTION_MANAGER),
		Kind:     types.MAIN_CONTEXT,
	})
	return guicontext.NewConnectionManagerContext(base, types.ContextTreeDeps{})
}

// dispatch invokes every binding matching the predicate against a registry
// the controller registered into.
func invokeMatching(t *testing.T, ctrl *controllers.ConnectionManagerController, match func(*types.ChordBinding) bool) {
	t.Helper()
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if match(kb) {
			if err := invokeAction(reg, kb); err != nil {
				t.Fatalf("invoke %q: %v", kb.ActionID, err)
			}
		}
	}
}

// TestConnectionManagerController_JKMovesCursor asserts j/k drive the modal
// context cursor in list mode (AC2).
func TestConnectionManagerController_JKMovesCursor(t *testing.T) {
	ctx := newModalCtx()
	ctx.SetItems([]any{
		&models.Connection{Name: "alpha"},
		&models.Connection{Name: "beta"},
		&models.Connection{Name: "gamma"},
	})
	ctrl := controllers.NewConnectionManagerController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, nil)
	ctrl.SetDeps(controllers.ConnectionManagerDeps{Ctx: ctx})

	invokeMatching(t, ctrl, func(b *types.ChordBinding) bool { return isRune(b, 'j') })
	if ctx.Cursor() != 1 {
		t.Fatalf("after j cursor = %d, want 1", ctx.Cursor())
	}
	invokeMatching(t, ctrl, func(b *types.ChordBinding) bool { return isRune(b, 'j') })
	if ctx.Cursor() != 2 {
		t.Fatalf("after jj cursor = %d, want 2", ctx.Cursor())
	}
	invokeMatching(t, ctrl, func(b *types.ChordBinding) bool { return isRune(b, 'k') })
	if ctx.Cursor() != 1 {
		t.Fatalf("after jjk cursor = %d, want 1", ctx.Cursor())
	}
}

// TestConnectionManagerController_GGAndGJump asserts gg jumps the cursor to
// the first profile and G jumps it to the last, in list mode.
func TestConnectionManagerController_GGAndGJump(t *testing.T) {
	ctx := newModalCtx()
	ctx.SetItems([]any{
		&models.Connection{Name: "alpha"},
		&models.Connection{Name: "beta"},
		&models.Connection{Name: "gamma"},
	})
	ctx.SetCursor(1)
	ctrl := controllers.NewConnectionManagerController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, nil)
	ctrl.SetDeps(controllers.ConnectionManagerDeps{Ctx: ctx})

	invokeMatching(t, ctrl, func(b *types.ChordBinding) bool { return isRune(b, 'G') })
	if ctx.Cursor() != 2 {
		t.Fatalf("after G cursor = %d, want 2", ctx.Cursor())
	}
	invokeMatching(t, ctrl, func(b *types.ChordBinding) bool { return isRuneSeq(b, 'g', 'g') })
	if ctx.Cursor() != 0 {
		t.Fatalf("after gg cursor = %d, want 0", ctx.Cursor())
	}
}

// TestConnectionManagerController_EnterConnectsSelected asserts <CR> in list
// mode invokes Connect with the selected profile (AC3).
func TestConnectionManagerController_EnterConnectsSelected(t *testing.T) {
	ctx := newModalCtx()
	ctx.SetItems([]any{
		&models.Connection{Name: "alpha"},
		&models.Connection{Name: "beta"},
	})
	ctx.SetCursor(1)

	var connected *models.Connection
	ctrl := controllers.NewConnectionManagerController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, nil)
	ctrl.SetDeps(controllers.ConnectionManagerDeps{
		Ctx:     ctx,
		Connect: func(p *models.Connection) { connected = p },
	})

	invokeMatching(t, ctrl, func(b *types.ChordBinding) bool { return isSpecial(b, types.KeyEnter) })
	if connected == nil || connected.Name != "beta" {
		t.Fatalf("Connect got %v, want beta", connected)
	}
}

// TestConnectionManagerController_EnterEmptyListNoConnect asserts <CR> with an
// empty list does not invoke Connect (AC4 — '[a] add' state).
func TestConnectionManagerController_EnterEmptyListNoConnect(t *testing.T) {
	ctx := newModalCtx()
	ctx.SetItems(nil)

	connects := 0
	ctrl := controllers.NewConnectionManagerController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, nil)
	ctrl.SetDeps(controllers.ConnectionManagerDeps{
		Ctx:     ctx,
		Connect: func(*models.Connection) { connects++ },
	})

	invokeMatching(t, ctrl, func(b *types.ChordBinding) bool { return isSpecial(b, types.KeyEnter) })
	if connects != 0 {
		t.Fatalf("Connect fired %d times on empty list, want 0", connects)
	}
}

// TestConnectionManagerController_ConnectingModeRetryAndCancel asserts that in
// connecting mode <CR>/r invoke Retry, <esc> invokes CancelConnecting (NOT the
// Close callback), and j/k are inert (AC3 / AC6).
func TestConnectionManagerController_ConnectingModeRetryAndCancel(t *testing.T) {
	ctx := newModalCtx()
	ctx.SetItems([]any{&models.Connection{Name: "alpha"}})
	ctx.SetMode(guicontext.ModeConnecting)

	retries, cancels, closes := 0, 0, 0
	ctrl := controllers.NewConnectionManagerController(nil, controllers.CoreDeps{}, controllers.UIDeps{},
		func() { closes++ })
	ctrl.SetDeps(controllers.ConnectionManagerDeps{
		Ctx:              ctx,
		Retry:            func() { retries++ },
		CancelConnecting: func() { cancels++ },
	})

	invokeMatching(t, ctrl, func(b *types.ChordBinding) bool { return isSpecial(b, types.KeyEnter) })
	invokeMatching(t, ctrl, func(b *types.ChordBinding) bool { return isRune(b, 'r') })
	if retries != 2 {
		t.Fatalf("Retry fired %d times (<CR>+r), want 2", retries)
	}

	invokeMatching(t, ctrl, func(b *types.ChordBinding) bool { return isSpecial(b, types.KeyEsc) })
	if cancels != 1 {
		t.Fatalf("CancelConnecting fired %d times, want 1", cancels)
	}
	if closes != 0 {
		t.Fatalf("Close must not fire in connecting mode; fired %d", closes)
	}

	// j in connecting mode is inert.
	before := ctx.Cursor()
	invokeMatching(t, ctrl, func(b *types.ChordBinding) bool { return isRune(b, 'j') })
	if ctx.Cursor() != before {
		t.Fatalf("j moved cursor in connecting mode: %d -> %d", before, ctx.Cursor())
	}
}

// TestConnectionManagerController_EscListModeCloses asserts <esc> in list mode
// dispatches the Close callback (root-exit semantics owned by the closure).
func TestConnectionManagerController_EscListModeCloses(t *testing.T) {
	ctx := newModalCtx()
	closes := 0
	ctrl := controllers.NewConnectionManagerController(nil, controllers.CoreDeps{}, controllers.UIDeps{},
		func() { closes++ })
	ctrl.SetDeps(controllers.ConnectionManagerDeps{Ctx: ctx})

	invokeMatching(t, ctrl, func(b *types.ChordBinding) bool { return isSpecial(b, types.KeyEsc) })
	if closes != 1 {
		t.Fatalf("Close fired %d times via esc in list mode, want 1", closes)
	}
}
