package orchestrator

import (
	"github.com/davesavic/dbsavvy/pkg/gui"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// isResultPaneKey reports whether k belongs to the PairNormal pane —
// i.e. the QueryEditor or any result tab. Result tabs share the
// RESULT_GRID context key so the cheatsheet and matcher resolve the
// bindings ResultTabsController publishes under that scope. The
// orchestrator's mid-query preempt only triggers when the user moves
// OUT of this pane (e.g. rail-switch to SCHEMAS / TABLES); transitions
// inside the pane (QueryEditor <-> RESULT_GRID) keep the active stream
// alive.
func isResultPaneKey(k types.ContextKey) bool {
	return k == types.QUERY_EDITOR || k == types.RESULT_GRID
}

// installResultTabsSwapHook wires a ContextTree swap hook that cancels
// the active result tab's stream when the user navigates AWAY from the
// QueryEditor / result-tab pane while a query is still Running. Queued
// tabs are NOT cancelled — they keep their queue slot. Within-pane
// transitions (editor <-> result tab) are also a no-op.
//
// Switching to a schema rail mid-query preempts via CancelRequest and
// marks the tab (cancelled, N rows) — this hook is the preempt site.
//
// The hook is registered after the existing matcher.Cancel / whichkey.Hide
// hooks so cancellation observably happens after the keystroke that
// triggered the pane switch has been processed. Hooks fire AFTER the
// swap on the same goroutine (MainLoop in production); we track the
// previous key via a closure variable since RegisterSwapHook's callback
// receives no arguments.
//
// Nil-safe: if tree or helper is nil, or helper.Active() returns nil,
// the hook is a no-op.
func installResultTabsSwapHook(tree *gui.ContextTree, helper *ui.ResultTabsHelper) {
	if tree == nil {
		return
	}
	var prev types.ContextKey
	tree.RegisterSwapHook(func() {
		var current types.ContextKey
		if c := tree.Current(); c != nil {
			current = c.GetKey()
		}
		was := prev
		prev = current

		// Bootstrap: first fire has empty `was` (no prior context). Nothing
		// to preempt.
		if was == "" {
			return
		}
		// Within-pane transition (e.g. QueryEditor -> RESULT_GRID): keep
		// the stream alive.
		if isResultPaneKey(was) && isResultPaneKey(current) {
			return
		}
		// Entering the pane from outside: nothing to cancel.
		if !isResultPaneKey(was) {
			return
		}
		// Leaving the pane. Only cancel if the active tab is actively
		// streaming — Queued / Complete / Cancelled / Errored / Plan tabs
		// are left alone.
		if helper == nil {
			return
		}
		active := helper.Active()
		if active == nil {
			return
		}
		if active.State() != ui.StateRunning {
			return
		}
		_ = helper.CancelActive()
	})
}
