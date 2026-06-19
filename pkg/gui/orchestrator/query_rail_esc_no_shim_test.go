package orchestrator_test

import (
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// TestQueryRailViewHasNoEscShim guards the fix for the bug where Escape
// never cleared visual-line (or insert) mode in the query editor.
//
// The QUERY_RAIL container (non-editable) and the editable QUERY_EDITOR
// leaf share ONE gocui view (QueryRailViewName == "query_editor",
// many-contexts-ONE-view topology). installKeyDispatch used to install an
// Esc-abort SetKeybinding on every non-editable view — including this
// shared one. gocui's execKeybindings checks view keybindings BEFORE
// delegating to the view's Editor (gui.go), so that shim consumed Escape
// and it never reached the QUERY_EDITOR VimEditor: visual.exit and
// mode.normal never fired.
//
// The shared view always has an editable master editor attached (the
// active leaf's), and that editor delivers Escape to the Matcher — which
// runs the chord-abort path itself. So the Esc shim is both redundant and
// harmful here and must NOT be installed.
func TestQueryRailViewHasNoEscShim(t *testing.T) {
	g, rec := buildTestGui(t)

	view := guicontext.QueryRailViewName // "query_editor", shared with editable leaf
	esc := gocui.NewKeyName(gocui.KeyEsc)

	// The editable QUERY_EDITOR leaf owns this view's input via its editor.
	if g.MasterEditorForTest(types.QUERY_EDITOR) == nil {
		t.Fatalf("no master editor for QUERY_EDITOR; the shared rail view has no editor to receive Escape")
	}

	// Structural guard: no Esc-abort shim may sit on the shared editable
	// view, or gocui consumes Escape before the editor sees it.
	if rec.HasKeybinding(view, esc, gocui.ModNone) {
		t.Fatalf("Esc shim registered on shared editable view %q — gocui consumes Escape "+
			"before the QUERY_EDITOR editor, so visual.exit / mode.normal never fire", view)
	}

	// Behavioural: the Matcher resolves Escape under QUERY_EDITOR scope, so
	// once the key reaches the editor (now unshadowed) the mode clears.
	g.Matcher().Cancel()
	if _, err := g.Matcher().Dispatch(types.QUERY_EDITOR, keys.Key{Code: 'V'}); err != nil {
		t.Fatalf("Dispatch(V) under QUERY_EDITOR: %v", err)
	}
	if got := g.Matcher().CurrentMode(types.QUERY_EDITOR); got != types.ModeVisualLine {
		t.Fatalf("after V: mode = %v, want ModeVisualLine", got)
	}
	if _, err := g.Matcher().Dispatch(types.QUERY_EDITOR, keys.Key{Special: keys.KeyEsc}); err != nil {
		t.Fatalf("Dispatch(<esc>) under QUERY_EDITOR: %v", err)
	}
	if got := g.Matcher().CurrentMode(types.QUERY_EDITOR); got != types.ModeNormal {
		t.Fatalf("after <esc>: mode = %v, want ModeNormal", got)
	}
}
