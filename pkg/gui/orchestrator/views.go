package orchestrator

import "github.com/davesavic/dbsavvy/pkg/gui/types"

// orderedViewNames is the canonical z-order (back-to-front) the Layout
// pass walks when calling SetView and SetViewOnTop. Side rails sit
// behind popups; the LIMIT overlay (terminal-too-small) sits at the
// front when active.
//
// Contexts whose Kind == STUB are present in the registry but filtered
// out of this walk via the Layout's per-iteration kind check (D11).
// Slots that are layout-only (status / options / main / secondary)
// don't correspond to registered Contexts at this epic and are
// intentionally absent from the list.
func orderedViewNames() []string {
	return []string{
		string(types.CONNECTIONS),
		string(types.SCHEMAS),
		string(types.TABLES),
		string(types.COLUMNS),
		string(types.INDEXES),
		string(types.LOG),
		string(types.MENU),
		string(types.CONFIRMATION),
		string(types.PROMPT),
		string(types.SUGGESTIONS),
		string(types.COMMAND_LINE),
		string(types.LIMIT),
	}
}
