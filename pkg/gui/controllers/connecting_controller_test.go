package controllers_test

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// TestConnectingControllerBindings asserts the controller publishes
// r → ConnectingRetry and <esc> → ConnectingCancel, both scoped to
// CONNECTING only.
func TestConnectingControllerBindings(t *testing.T) {
	ctrl := controllers.NewConnectingController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, nil, nil)

	hasRetry, hasCancel := false, false
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if kb.Scope != types.CONNECTING {
			t.Errorf("binding scope = %q, want CONNECTING", kb.Scope)
		}
		if isRune(kb, 'r') && kb.ActionID == commands.ConnectingRetry {
			hasRetry = true
		}
		if isSpecial(kb, types.KeyEsc) && kb.ActionID == commands.ConnectingCancel {
			hasCancel = true
		}
	}
	if !hasRetry || !hasCancel {
		t.Fatalf("connecting bindings missing: retry=%v cancel=%v", hasRetry, hasCancel)
	}
}

// TestConnectingControllerRetryInvokesCallback asserts pressing r invokes
// the injected Retry callback exactly once.
func TestConnectingControllerRetryInvokesCallback(t *testing.T) {
	retries, cancels := 0, 0
	ctrl := controllers.NewConnectingController(nil, controllers.CoreDeps{}, controllers.UIDeps{},
		func() { retries++ }, func() { cancels++ })
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if isRune(kb, 'r') {
			if err := invokeAction(reg, kb); err != nil {
				t.Fatalf("r: %v", err)
			}
		}
	}
	if retries != 1 {
		t.Fatalf("Retry called %d times via r, want 1", retries)
	}
	if cancels != 0 {
		t.Fatalf("Cancel called %d times via r, want 0", cancels)
	}
}

// TestConnectingControllerCancelInvokesCallback asserts pressing <esc>
// invokes the injected Cancel callback exactly once.
func TestConnectingControllerCancelInvokesCallback(t *testing.T) {
	retries, cancels := 0, 0
	ctrl := controllers.NewConnectingController(nil, controllers.CoreDeps{}, controllers.UIDeps{},
		func() { retries++ }, func() { cancels++ })
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if isSpecial(kb, types.KeyEsc) {
			if err := invokeAction(reg, kb); err != nil {
				t.Fatalf("esc: %v", err)
			}
		}
	}
	if cancels != 1 {
		t.Fatalf("Cancel called %d times via esc, want 1", cancels)
	}
	if retries != 0 {
		t.Fatalf("Retry called %d times via esc, want 0", retries)
	}
}

// TestConnectingControllerNilCallbacksAreSafe asserts the handlers no-op
// rather than panic when no callbacks are wired.
func TestConnectingControllerNilCallbacksAreSafe(t *testing.T) {
	ctrl := controllers.NewConnectingController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, nil, nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if err := invokeAction(reg, kb); err != nil {
			t.Fatalf("invoke %q with nil callbacks: %v", kb.ActionID, err)
		}
	}
}
