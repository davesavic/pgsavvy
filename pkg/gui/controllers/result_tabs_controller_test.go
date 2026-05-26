package controllers_test

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// TestResultTabsControllerGBindingJumpsLast guards the dbsavvy-6t9
// regression: `G` in the result grid must dispatch ResultJumpLast (jump
// cursor to the last loaded row, symmetric with gg=ResultJumpFirst). It
// was previously bound to ResultReadToEnd, whose handler no-ops in the
// default grid view once the stream is complete — so `G` appeared to do
// nothing while `gg` worked. `]G` keeps the explicit drain-to-end.
func TestResultTabsControllerGBindingJumpsLast(t *testing.T) {
	b := newBag()
	ctrl := controllers.NewResultTabsController(nil, b.HelperBag, nil)

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
