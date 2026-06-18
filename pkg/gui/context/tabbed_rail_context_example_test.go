package context

import (
	"fmt"

	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// ExampleNewTabbedRailContext shows how cheaply a THIRD tabbed-pane consumer is
// wired: declare the tabs, construct the container, inject the leaves, switch
// tabs. The container handles the tab strip, per-tab origin, focus hooks, and
// dirty-flush — the consumer adds nothing.
func ExampleNewTabbedRailContext() {
	drv := testfake.NewRecorderGuiDriver()

	// The container owns one shared view; declare its tabs (leaf keys reused
	// here only as stable identities for the demo).
	base := NewBaseContext(BaseContextOpts{
		Key:      types.QUERY_EDITOR,
		ViewName: "demo_rail",
		Kind:     types.MAIN_CONTEXT,
	})
	rail := NewTabbedRailContext(base, Deps{GuiDriver: drv}, TabbedRailOpts{FireFocusHooks: true},
		TabSpec{Label: "Editor", LeafKey: types.QUERY_EDITOR, ManagesOwnOrigin: true},
		TabSpec{Label: "History", LeafKey: types.HISTORY},
	)

	// Leaves are constructed separately, then injected positionally.
	editor := newFakeLeaf(types.QUERY_EDITOR)
	history := newFakeLeaf(types.HISTORY)
	rail.SetLeaves(editor, history)

	fmt.Println("active:", rail.ActiveLeafKey())
	rail.SetActiveTab(1)
	fmt.Println("active:", rail.ActiveLeafKey())

	// The switch fired exactly one outgoing focus-lost + one incoming focus.
	fmt.Println("editor focusLost:", editor.focusLostCount)
	fmt.Println("history focus:", history.focusCount)

	// Output:
	// active: query_editor
	// active: history
	// editor focusLost: 1
	// history focus: 1
}
