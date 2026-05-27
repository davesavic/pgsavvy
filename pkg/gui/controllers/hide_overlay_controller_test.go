package controllers_test

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// TestHideOverlayControllerCtrlNAndCtrlPBindings asserts <C-n> dispatches
// HideOverlayDown and <C-p> dispatches HideOverlayUp, mirroring the
// j/<down> and k/<up> bindings per AD-9 (dbsavvy-56u.2).
func TestHideOverlayControllerCtrlNAndCtrlPBindings(t *testing.T) {
	b := newBag()
	ctrl := controllers.NewHideOverlayController(nil, b.HelperBag.CoreDeps, nil)
	hasCtrlN, hasCtrlP := false, false
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if kb.Scope != types.HIDE_OVERLAY {
			continue
		}
		if len(kb.Sequence) != 1 {
			continue
		}
		k := kb.Sequence[0]
		if k.Code == 'n' && k.Mod == types.ChordModCtrl && kb.ActionID == commands.HideOverlayDown {
			hasCtrlN = true
		}
		if k.Code == 'p' && k.Mod == types.ChordModCtrl && kb.ActionID == commands.HideOverlayUp {
			hasCtrlP = true
		}
	}
	if !hasCtrlN || !hasCtrlP {
		t.Fatalf("hide-overlay bindings missing: <C-n>=%v <C-p>=%v", hasCtrlN, hasCtrlP)
	}
}
