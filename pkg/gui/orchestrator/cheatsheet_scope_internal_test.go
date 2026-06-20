package orchestrator

import (
	"testing"

	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// newRailContainer builds a QUERY_RAIL-style tabbed container whose GetKey()
// returns the container key but whose ActiveLeafKey() returns the active leaf.
// Specs alone populate the tab leafKeys, so ActiveLeafKey resolves without
// SetLeaves (mirrors the example in pkg/gui/context).
func newRailContainer(containerKey types.ContextKey, specs ...guicontext.TabSpec) *guicontext.TabbedRailContext {
	base := guicontext.NewBaseContext(guicontext.BaseContextOpts{
		Key:      containerKey,
		ViewName: guicontext.QueryRailViewName,
		Kind:     types.MAIN_CONTEXT,
	})
	return guicontext.NewTabbedRailContext(base, guicontext.Deps{}, guicontext.TabbedRailOpts{FireFocusHooks: true}, specs...)
}

// newPlainLeaf builds a non-tabbed context (no ActiveLeafKey method) keyed to
// scope — the simple case where cheatsheetScope returns the key verbatim.
func newPlainLeaf(scope types.ContextKey) types.IBaseContext {
	base := guicontext.NewBaseContext(guicontext.BaseContextOpts{
		Key:      scope,
		ViewName: string(scope),
		Kind:     types.DISPLAY_CONTEXT,
	})
	return guicontext.NewDisplayLeafContext(base, guicontext.Deps{}, string(scope), "")
}

// TestCheatsheetScope is the regression guard for pgsavvy-2pix: opening the
// cheatsheet while a tabbed-rail container (QUERY_RAIL) is focused must resolve
// to the ACTIVE leaf's scope (QUERY_EDITOR), not the container key — otherwise
// the editor/visual-mode bindings never surface in the cheatsheet.
func TestCheatsheetScope(t *testing.T) {
	t.Run("QueryRailDescendsToActiveEditorLeaf", func(t *testing.T) {
		rail := newRailContainer(types.QUERY_RAIL,
			guicontext.TabSpec{Label: "Editor", LeafKey: types.QUERY_EDITOR, ManagesOwnOrigin: true},
			guicontext.TabSpec{Label: "History", LeafKey: types.HISTORY},
		)
		if got := cheatsheetScope(rail); got != types.QUERY_EDITOR {
			t.Fatalf("editor tab active: got scope %q, want %q", got, types.QUERY_EDITOR)
		}
	})

	t.Run("QueryRailDescendsToActiveNonEditorLeaf", func(t *testing.T) {
		rail := newRailContainer(types.QUERY_RAIL,
			guicontext.TabSpec{Label: "Editor", LeafKey: types.QUERY_EDITOR, ManagesOwnOrigin: true},
			guicontext.TabSpec{Label: "History", LeafKey: types.HISTORY},
		)
		rail.SetActiveTab(1)
		if got := cheatsheetScope(rail); got != types.HISTORY {
			t.Fatalf("history tab active: got scope %q, want %q", got, types.HISTORY)
		}
	})

	t.Run("PlainContextReturnsOwnKey", func(t *testing.T) {
		if got := cheatsheetScope(newPlainLeaf(types.TABLES)); got != types.TABLES {
			t.Fatalf("plain context: got scope %q, want %q", got, types.TABLES)
		}
	})

	t.Run("EmptyRailFallsBackToContainerKey", func(t *testing.T) {
		rail := newRailContainer(types.QUERY_RAIL) // no tabs -> ActiveLeafKey == ""
		if got := cheatsheetScope(rail); got != types.QUERY_RAIL {
			t.Fatalf("tab-less rail: got scope %q, want %q", got, types.QUERY_RAIL)
		}
	})

	t.Run("NilTopCollapsesToGlobal", func(t *testing.T) {
		if got := cheatsheetScope(nil); got != types.GLOBAL {
			t.Fatalf("nil top: got scope %q, want %q", got, types.GLOBAL)
		}
	})
}
