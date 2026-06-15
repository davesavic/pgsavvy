package controllers_test

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
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

// gg jumps the cursor to the first row; G jumps it to the last row.
// Exercised via the schemas controller which embeds the trait.
func TestListControllerTraitJumpFirstAndLast(t *testing.T) {
	b := newBag()
	cur := &fakeCursor{idx: 3, items: []any{1, 2, 3, 4, 5, 6, 7}}
	ctrl := controllers.NewSchemasController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, b.HelperBag.UIDeps, cur, b.SchemaPicker)
	reg := commands.NewRegistry()
	ctrl.ListControllerTrait.RegisterActions(reg)
	bindings := ctrl.GetKeybindings(types.KeybindingsOpts{})

	ggFired, gFired := false, false
	for _, kb := range bindings {
		if isRuneSeq(kb, 'g', 'g') {
			if err := invokeAction(reg, kb); err != nil {
				t.Fatalf("gg: %v", err)
			}
			if cur.idx != 0 {
				t.Fatalf("cursor idx after gg = %d, want 0", cur.idx)
			}
			ggFired = true
		}
		if isRune(kb, 'G') {
			if err := invokeAction(reg, kb); err != nil {
				t.Fatalf("G: %v", err)
			}
			if cur.idx != 6 {
				t.Fatalf("cursor idx after G = %d, want 6", cur.idx)
			}
			gFired = true
		}
	}
	if !ggFired || !gFired {
		t.Fatalf("expected both gg and G bindings, ggFired=%v gFired=%v", ggFired, gFired)
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
