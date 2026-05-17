package controllers_test

import (
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

func TestMenuControllerEscPopsMenu(t *testing.T) {
	b := newBag()
	ctrl := controllers.NewMenuController(nil, b.HelperBag)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if kb.Key.Equals(gocui.NewKeyName(gocui.KeyEsc)) {
			if err := kb.Handler(); err != nil {
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
		if kb.Key.Equals(gocui.NewKeyName(gocui.KeyEnter)) {
			hasEnter = true
		}
		if kb.Key.Equals(gocui.NewKeyName(gocui.KeyEsc)) {
			hasEsc = true
		}
	}
	if !hasEnter || !hasEsc {
		t.Fatalf("menu bindings missing: enter=%v esc=%v", hasEnter, hasEsc)
	}
}
