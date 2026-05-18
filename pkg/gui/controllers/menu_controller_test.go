package controllers_test

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

func TestMenuControllerEscPopsMenu(t *testing.T) {
	b := newBag()
	ctrl := controllers.NewMenuController(nil, b.HelperBag)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if isSpecial(kb, types.KeyEsc) {
			if err := invokeAction(reg, kb); err != nil {
				t.Fatalf("esc: %v", err)
			}
		}
	}
	if b.Menu.popped != 1 {
		t.Fatalf("Menu.PopMenu = %d, want 1", b.Menu.popped)
	}
}

func TestMenuControllerHasEnterAndEsc(t *testing.T) {
	b := newBag()
	ctrl := controllers.NewMenuController(nil, b.HelperBag)
	hasEnter, hasEsc := false, false
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if isSpecial(kb, types.KeyEnter) {
			hasEnter = true
		}
		if isSpecial(kb, types.KeyEsc) {
			hasEsc = true
		}
	}
	if !hasEnter || !hasEsc {
		t.Fatalf("menu bindings missing: enter=%v esc=%v", hasEnter, hasEsc)
	}
}
