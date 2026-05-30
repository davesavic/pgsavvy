package controllers_test

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// The trait's j/k handlers drive SideListCursor by ±1 each invocation.
// We exercise via the connections controller which embeds the trait.
func TestListControllerTraitDownAndUp(t *testing.T) {
	b := newBag()
	cur := &fakeCursor{idx: 5, items: []any{1, 2, 3, 4, 5, 6, 7}}
	ctrl := controllers.NewSchemasController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, b.HelperBag.UIDeps, cur, b.SchemaPicker)
	reg := commands.NewRegistry()
	ctrl.ListControllerTrait.RegisterActions(reg)
	bindings := ctrl.GetKeybindings(types.KeybindingsOpts{})

	jFired, kFired := false, false
	for _, kb := range bindings {
		if isRune(kb, 'j') {
			if err := invokeAction(reg, kb); err != nil {
				t.Fatalf("j: %v", err)
			}
			jFired = true
		}
		if isRune(kb, 'k') {
			if err := invokeAction(reg, kb); err != nil {
				t.Fatalf("k: %v", err)
			}
			kFired = true
		}
	}
	if !jFired || !kFired {
		t.Fatalf("expected both j and k bindings, jFired=%v kFired=%v", jFired, kFired)
	}
	if cur.idx != 5 {
		t.Fatalf("cursor idx after j,k = %d, want 5", cur.idx)
	}
}

// The trait <CR> binding fires the controller-supplied onConfirm.
func TestListControllerTraitConfirmDelegates(t *testing.T) {
	b := newBag()
	cur := &fakeCursor{}
	ctrl := controllers.NewTablesController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, cur, b.TablePicker)
	reg := commands.NewRegistry()
	ctrl.ListControllerTrait.RegisterActions(reg)
	bindings := ctrl.GetKeybindings(types.KeybindingsOpts{})
	for _, kb := range bindings {
		if isSpecial(kb, types.KeyEnter) {
			if err := invokeAction(reg, kb); err != nil {
				t.Fatalf("<CR> on tables: %v", err)
			}
		}
	}
}
