package orchestrator_test

import (
	"errors"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// TestLeaderQQuitsFromQueryEditor is the dbsavvy-dg5 regression test.
//
// When the QUERY_EDITOR view is focused, gocui routes keystrokes through
// the VimEditor's gocui.Editor (Edit), NOT through the SetKeybinding
// shims used by list contexts. The default `<leader>q` (space + q) is a
// GLOBAL-scope binding mapped to app.quit, whose handler returns
// gocui.ErrQuit. Matcher.Dispatch resolves the chord via scope→GLOBAL
// fall-through and returns (Dispatched, gocui.ErrQuit).
//
// gocui.Editor.Edit can only return a bool, so the editor must reschedule
// the quit onto the main loop via the GuiDriver (mirroring
// masterEditor.Edit). The recorder driver runs the queued closure and
// records its returned error, so a successful reschedule surfaces
// gocui.ErrQuit in UpdateErrors.
//
// Before the fix, VimEditor.Edit discarded the Dispatch error and held no
// GuiDriver, so ErrQuit was silently dropped and the app never quit.
func TestLeaderQQuitsFromQueryEditor(t *testing.T) {
	g, rec := buildTestGui(t)

	ed := g.MasterEditorForTest(types.QUERY_EDITOR)
	if ed == nil {
		t.Fatal("no master editor installed for QUERY_EDITOR")
	}

	// Drive `<leader>q` = space then q through the focused-editor path.
	ed.Edit(nil, gocui.NewKeyRune(' '))
	ed.Edit(nil, gocui.NewKeyRune('q'))

	errs := rec.UpdateErrors()
	for _, e := range errs {
		if errors.Is(e, gocui.ErrQuit) {
			return // quit was rescheduled — pass
		}
	}
	t.Fatalf("leader-q from the query editor did not reschedule gocui.ErrQuit "+
		"(VimEditor.Edit dropped the Dispatch error); UpdateErrors=%v", errs)
}
