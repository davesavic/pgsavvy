package controllers_test

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// AttachControllers must attach a binding contributor to every primary
// context. Verified by asking the context's GetKeybindings to produce
// a non-empty result after attachment.
func TestAttachControllersWiresEveryContext(t *testing.T) {
	tree := context.NewContextTree(types.ContextTreeDeps{})
	b := newBag()

	got := controllers.AttachControllers(tree, nil, b.HelperBag)
	if got == nil {
		t.Fatal("AttachControllers returned nil bundle")
	}
	if got.Schemas == nil || got.Tables == nil ||
		got.Menu == nil ||
		got.Prompt == nil || got.Selection == nil || got.Quit == nil {
		t.Fatalf("Controllers bundle has nil entries: %+v", got)
	}

	for _, ctx := range []types.IBaseContext{
		tree.Schemas, tree.Tables,
		tree.Menu, tree.Prompt, tree.Selection, tree.Global,
	} {
		kbs := ctx.GetKeybindings(types.KeybindingsOpts{})
		if len(kbs) == 0 {
			t.Errorf("context %q has no bindings after AttachControllers", ctx.GetKey())
		}
	}
}

// Nil tree returns an empty bundle without panicking.
func TestAttachControllersNilTreeReturnsEmptyBundle(t *testing.T) {
	got := controllers.AttachControllers(nil, nil, controllers.HelperBag{})
	if got == nil {
		t.Fatal("AttachControllers(nil) returned nil")
	}
	if got.Schemas != nil || got.Quit != nil {
		t.Fatal("nil tree: bundle entries should be nil")
	}
}
