package controllers_test

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// TestConfirmationControllerYesAndNoBindings asserts the controller
// publishes y/<cr> → Yes and n/<esc> → No bindings under CONFIRMATION
// scope.
func TestConfirmationControllerYesAndNoBindings(t *testing.T) {
	b := newBag()
	ctrl := controllers.NewConfirmationController(nil, b.HelperBag)

	hasYRune, hasEnter, hasNRune, hasEsc := false, false, false, false
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if kb.Scope != types.CONFIRMATION {
			t.Errorf("binding scope = %q, want CONFIRMATION", kb.Scope)
		}
		if isRune(kb, 'y') && kb.ActionID == commands.ConfirmYes {
			hasYRune = true
		}
		if isSpecial(kb, types.KeyEnter) && kb.ActionID == commands.ConfirmYes {
			hasEnter = true
		}
		if isRune(kb, 'n') && kb.ActionID == commands.ConfirmNo {
			hasNRune = true
		}
		if isSpecial(kb, types.KeyEsc) && kb.ActionID == commands.ConfirmNo {
			hasEsc = true
		}
	}
	if !hasYRune || !hasEnter || !hasNRune || !hasEsc {
		t.Fatalf("confirmation bindings missing: y=%v enter=%v n=%v esc=%v",
			hasYRune, hasEnter, hasNRune, hasEsc)
	}
}

// TestConfirmationControllerYesDispatchesToHelper asserts pressing y or
// <cr> invokes ConfirmHelper.Yes exactly once.
func TestConfirmationControllerYesDispatchesToHelper(t *testing.T) {
	b := newBag()
	ctrl := controllers.NewConfirmationController(nil, b.HelperBag)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if isRune(kb, 'y') {
			if err := invokeAction(reg, kb); err != nil {
				t.Fatalf("y: %v", err)
			}
		}
	}
	if b.Confirm.yes != 1 {
		t.Fatalf("Confirm.Yes called %d times via y, want 1", b.Confirm.yes)
	}
}

// TestConfirmationControllerNoDispatchesToHelper asserts pressing n or
// <esc> invokes ConfirmHelper.No exactly once.
func TestConfirmationControllerNoDispatchesToHelper(t *testing.T) {
	b := newBag()
	ctrl := controllers.NewConfirmationController(nil, b.HelperBag)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if isSpecial(kb, types.KeyEsc) {
			if err := invokeAction(reg, kb); err != nil {
				t.Fatalf("esc: %v", err)
			}
		}
	}
	if b.Confirm.no != 1 {
		t.Fatalf("Confirm.No called %d times via esc, want 1", b.Confirm.no)
	}
}

// TestConfirmationControllerNilHelperIsSafe asserts the handlers no-op
// rather than panic when no ConfirmHelper is wired.
func TestConfirmationControllerNilHelperIsSafe(t *testing.T) {
	bag := controllers.HelperBag{} // no Confirm helper
	ctrl := controllers.NewConfirmationController(nil, bag)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if err := invokeAction(reg, kb); err != nil {
			t.Fatalf("invoke %q with nil helper: %v", kb.ActionID, err)
		}
	}
}
