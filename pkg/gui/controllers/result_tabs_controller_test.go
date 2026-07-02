package controllers_test

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// TestResultTabsControllerGBindingJumpsLast guards the regression:
// `G` in the result grid must dispatch ResultJumpLast (jump
// cursor to the last loaded row, symmetric with gg=ResultJumpFirst). It
// was previously bound to ResultReadToEnd, whose handler no-ops in the
// default grid view once the stream is complete — so `G` appeared to do
// nothing while `gg` worked. `]G` keeps the explicit drain-to-end.
func TestResultTabsControllerGBindingJumpsLast(t *testing.T) {
	b := newBag()
	ctrl := controllers.NewResultTabsController(nil, b.HelperBag.CoreDeps, b.HelperBag.UIDeps, b.HelperBag.EditDeps, nil)

	found := false
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if kb.Scope != types.RESULT_GRID || len(kb.Sequence) != 1 {
			continue
		}
		k := kb.Sequence[0]
		if k.Code == 'G' && k.Special == types.KeyNone {
			if kb.ActionID != commands.ResultJumpLast {
				t.Fatalf("G binding ActionID = %q, want %q", kb.ActionID, commands.ResultJumpLast)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no single-rune G binding found in RESULT_GRID scope")
	}
}

// TestResultTabsControllerYankBindings guards pgsavvy U4: `y` yanks the
// focused cell (ResultYankCell) and `yy` yanks the focused row
// (ResultYankRow), both RESULT_GRID-scoped, Normal mode.
func TestResultTabsControllerYankBindings(t *testing.T) {
	b := newBag()
	ctrl := controllers.NewResultTabsController(nil, b.HelperBag.CoreDeps, b.HelperBag.UIDeps, b.HelperBag.EditDeps, nil)

	var foundCell, foundRow bool
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if kb.Scope != types.RESULT_GRID {
			continue
		}
		// `y` — single bare-rune sequence.
		if len(kb.Sequence) == 1 {
			k := kb.Sequence[0]
			if k.Code == 'y' && k.Special == types.KeyNone {
				if kb.ActionID != commands.ResultYankCell {
					t.Fatalf("y binding ActionID = %q, want %q", kb.ActionID, commands.ResultYankCell)
				}
				if kb.Mode != types.ModeNormal {
					t.Fatalf("y binding Mode = %v, want ModeNormal", kb.Mode)
				}
				foundCell = true
			}
		}
		// `yy` — two-rune sequence.
		if len(kb.Sequence) == 2 {
			k0, k1 := kb.Sequence[0], kb.Sequence[1]
			if k0.Code == 'y' && k1.Code == 'y' && k0.Special == types.KeyNone && k1.Special == types.KeyNone {
				if kb.ActionID != commands.ResultYankRow {
					t.Fatalf("yy binding ActionID = %q, want %q", kb.ActionID, commands.ResultYankRow)
				}
				foundRow = true
			}
		}
	}
	if !foundCell {
		t.Fatal("no `y` (ResultYankCell) binding found in RESULT_GRID scope")
	}
	if !foundRow {
		t.Fatal("no `yy` (ResultYankRow) binding found in RESULT_GRID scope")
	}

	var foundSelectCell bool
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if kb.Scope != types.RESULT_GRID || len(kb.Sequence) != 1 {
			continue
		}
		k := kb.Sequence[0]
		if k.Code == 'v' && k.Special == types.KeyNone && k.Mod == 0 {
			if kb.ActionID != commands.ResultSelectCell {
				t.Fatalf("v binding ActionID = %q, want %q", kb.ActionID, commands.ResultSelectCell)
			}
			if kb.Mode != types.ModeNormal {
				t.Fatalf("v binding Mode = %v, want ModeNormal", kb.Mode)
			}
			foundSelectCell = true
		}
	}
	if !foundSelectCell {
		t.Fatal("no `v` (ResultSelectCell) binding found in RESULT_GRID scope")
	}
}
