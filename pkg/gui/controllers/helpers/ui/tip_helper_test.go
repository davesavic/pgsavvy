package ui_test

import (
	"testing"

	"github.com/spf13/afero"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
)

func TestDismissStartupTipStampsAndPops(t *testing.T) {
	store := common.NewAppStateStore(afero.NewMemMapFs(), "/state.yml", nil)
	t.Cleanup(func() { _ = store.Close() })

	tree := gui.NewContextTree()
	pushRoot(t, tree)
	// Push a popup so Pop has something to remove (the "tip popup").
	pushRoot(t, tree)

	h := ui.NewTipHelper(tree, store)
	if err := h.DismissStartupTip(); err != nil {
		t.Fatalf("DismissStartupTip: %v", err)
	}
	if !store.IsStartupTipsSeen() {
		t.Fatal("IsStartupTipsSeen = false after DismissStartupTip")
	}
}

func TestDismissStartupTipSwallowsEmptyStackPop(t *testing.T) {
	store := common.NewAppStateStore(afero.NewMemMapFs(), "/state.yml", nil)
	t.Cleanup(func() { _ = store.Close() })
	tree := gui.NewContextTree()
	pushRoot(t, tree)

	h := ui.NewTipHelper(tree, store)
	// Only a root present; Pop returns ErrPopAtBottom which the helper
	// MUST swallow per the post-auto-dismiss path documented on the
	// helper.
	if err := h.DismissStartupTip(); err != nil {
		t.Fatalf("DismissStartupTip with empty popup stack: %v", err)
	}
	if !store.IsStartupTipsSeen() {
		t.Fatal("seen-at not stamped on no-popup path")
	}
}

func TestDismissStartupTipNilFieldsAreSafe(t *testing.T) {
	h := ui.NewTipHelper(nil, nil)
	if err := h.DismissStartupTip(); err != nil {
		t.Fatalf("DismissStartupTip with nil deps: %v", err)
	}
}
