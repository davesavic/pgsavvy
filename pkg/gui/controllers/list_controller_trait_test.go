package controllers_test

import (
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// The trait's j/k handlers drive SideListCursor by ±1 each invocation.
// We exercise via the connections controller which embeds the trait.
func TestListControllerTraitDownAndUp(t *testing.T) {
	b := newBag()
	cur := &fakeCursor{idx: 5, items: []any{1, 2, 3, 4, 5, 6, 7}}
	ctrl := controllers.NewConnectionsController(nil, b.HelperBag, cur, b.ConnPicker)
	bindings := ctrl.GetKeybindings(types.KeybindingsOpts{})

	jFired, kFired := false, false
	for _, kb := range bindings {
		if kb.Key.Equals(gocui.NewKeyRune('j')) {
			if err := kb.Handler(); err != nil {
				t.Fatalf("j: %v", err)
			}
			jFired = true
		}
		if kb.Key.Equals(gocui.NewKeyRune('k')) {
			if err := kb.Handler(); err != nil {
				t.Fatalf("k: %v", err)
			}
			kFired = true
		}
	}
	if !jFired || !kFired {
		t.Fatalf("expected both j and k bindings, jFired=%v kFired=%v", jFired, kFired)
	}
	// Net: +1 then -1 -> idx unchanged at 5. SetCursor clamps to len-1
	// so after j (->6) and k (->5) we end at 5.
	if cur.idx != 5 {
		t.Fatalf("cursor idx after j,k = %d, want 5", cur.idx)
	}
}

// The trait <CR> binding fires the controller-supplied onConfirm.
func TestListControllerTraitConfirmDelegates(t *testing.T) {
	b := newBag()
	cur := &fakeCursor{}
	// Use the columns controller (no row activation -> onConfirm is a
	// noop returning nil; we just need a binding to fire).
	ctrl := controllers.NewColumnsController(nil, b.HelperBag, cur)
	bindings := ctrl.GetKeybindings(types.KeybindingsOpts{})
	for _, kb := range bindings {
		if kb.Key.Equals(gocui.NewKeyName(gocui.KeyEnter)) {
			if err := kb.Handler(); err != nil {
				t.Fatalf("<CR> on columns: %v", err)
			}
		}
	}
}
