package context

import "github.com/davesavic/pgsavvy/pkg/gui/types"

// QueryRailViewName is the single gocui view the QUERY_RAIL container and
// all three leaves (QUERY_EDITOR, SAVED_QUERY, HISTORY) render into
// (many-contexts-ONE-view topology). It reuses the historical query-editor
// view name so the existing layout/view wiring (editor.Buffer paint, FocusPoint
// scroll, completion-popup anchor) keeps targeting the same view.
const QueryRailViewName = "query_editor"

// QueryRailContext is the tabbed container for the query-editor pane. It is a
// THIN ADAPTER over the shared TabbedRailContext core (many-contexts-ONE-view
// topology): an editable editor leaf alongside stateless list leaves
// (QUERY_EDITOR, SAVED_QUERY, HISTORY) multiplexed into a single shared gocui
// view. All tabbed-pane mechanics (tab switching, focus-hook firing, per-tab
// origin save/restore, tab-strip publishing, tab_switch logging) live in the
// embedded core; this type exists only so the registry can hold a stable
// *QueryRailContext and so the construction shims below keep their public
// surface.
//
// The QUERY_RAIL constructs the core with FireFocusHooks=TRUE: its leaves live
// under DISTINCT master-editor scopes, so a tab switch is a real focus
// transition and each leaf must run its own focus protocol (e.g. the editor
// leaf flipping its mode to Normal). The editor tab opts OUT of the generic
// origin save/restore (ManagesOwnOrigin) — the editor drives its own scroll
// every frame, and restoring a saved origin would fight that.
type QueryRailContext struct {
	*TabbedRailContext
}

// QueryRailTabSpec declares a tab at construction time, mirroring the core
// TabSpec. Retained as a shim so existing construction callers (setup.go and
// the query_rail tests) keep compiling against the QUERY_RAIL-specific name.
// ManagesOwnOrigin must be set for the editor leaf so the container's origin
// save/restore machinery skips it.
type QueryRailTabSpec struct {
	Label            string
	LeafKey          types.ContextKey
	ManagesOwnOrigin bool
}

// dirtyFlusher is the minimal flush seam the editor leaf exposes
// (QueryEditorContext.FlushDirty) so the container can flush unsaved edits
// on focus loss without depending on the concrete editor type. Consumed by
// the embedded TabbedRailContext core; defined here as it is QUERY_RAIL's
// historical seam.
type dirtyFlusher interface {
	FlushDirty() error
}

// NewQueryRailContext builds the QUERY_RAIL container as a thin adapter over a
// TabbedRailContext core constructed with FireFocusHooks=TRUE. The active tab
// defaults to 0. Leaf references are injected via the promoted SetLeaves once
// all contexts are constructed.
func NewQueryRailContext(base BaseContext, deps Deps, specs ...QueryRailTabSpec) *QueryRailContext {
	core := NewTabbedRailContext(base, deps, TabbedRailOpts{FireFocusHooks: true}, toTabSpecs(specs)...)
	return &QueryRailContext{TabbedRailContext: core}
}

// toTabSpecs maps the QUERY_RAIL-specific tab specs onto the core TabSpec type.
func toTabSpecs(specs []QueryRailTabSpec) []TabSpec {
	out := make([]TabSpec, len(specs))
	for i, s := range specs {
		out[i] = TabSpec(s)
	}
	return out
}
