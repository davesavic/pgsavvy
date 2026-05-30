package controllers_test

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// TestConnectionManagerControllerBindings asserts the controller publishes
// <esc> → ConnectionManagerClose and q → AppQuit, both scoped to
// CONNECTION_MANAGER. q quits because the modal is the startup root (ig4);
// it must NOT map to the Close action. Ctrl-C quits via the GLOBAL-scope
// binding owned by QuitController.
func TestConnectionManagerControllerBindings(t *testing.T) {
	ctrl := controllers.NewConnectionManagerController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, nil)

	hasClose := false
	hasQuit := false
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if kb.Scope != types.CONNECTION_MANAGER {
			t.Errorf("binding scope = %q, want CONNECTION_MANAGER", kb.Scope)
		}
		if isSpecial(kb, types.KeyEsc) && kb.ActionID == commands.ConnectionManagerClose {
			hasClose = true
		}
		if isRune(kb, 'q') {
			if kb.ActionID != commands.AppQuit {
				t.Errorf("q binding ActionID = %q, want AppQuit", kb.ActionID)
			}
			if kb.ActionID == commands.ConnectionManagerClose {
				t.Errorf("q must not map to the Close action")
			}
			hasQuit = true
		}
	}
	if !hasClose {
		t.Fatal("connection manager close binding missing")
	}
	if !hasQuit {
		t.Fatal("connection manager q → AppQuit binding missing")
	}
}

// TestConnectionManagerControllerCloseInvokesCallback asserts pressing <esc>
// invokes the injected Close callback exactly once.
func TestConnectionManagerControllerCloseInvokesCallback(t *testing.T) {
	closes := 0
	ctrl := controllers.NewConnectionManagerController(nil, controllers.CoreDeps{}, controllers.UIDeps{},
		func() { closes++ })
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if isSpecial(kb, types.KeyEsc) {
			if err := invokeAction(reg, kb); err != nil {
				t.Fatalf("esc: %v", err)
			}
		}
	}
	if closes != 1 {
		t.Fatalf("Close called %d times via esc, want 1", closes)
	}
}

// TestConnectionManagerControllerNilCallbackIsSafe asserts the handler
// no-ops rather than panic when no callback is wired (root-exit semantics
// are owned by the injected closure; an unwired closure must not crash).
func TestConnectionManagerControllerNilCallbackIsSafe(t *testing.T) {
	ctrl := controllers.NewConnectionManagerController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if err := invokeAction(reg, kb); err != nil {
			t.Fatalf("invoke %q with nil callback: %v", kb.ActionID, err)
		}
	}
}
