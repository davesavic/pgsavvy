package context

import (
	"log/slog"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/logs"
)

// QueryRailViewName is the single gocui view the QUERY_RAIL container and
// all three leaves (QUERY_EDITOR, SAVED_QUERY, HISTORY) render into
// (many-contexts-ONE-view topology). It reuses the historical query-editor
// view name so the existing layout/view wiring (editor.Buffer paint, FocusPoint
// scroll, completion-popup anchor) keeps targeting the same view.
const QueryRailViewName = "query_editor"

// QueryRailContext is the tabbed container for the query-editor pane. It
// multiplexes a set of HETEROGENEOUS leaf contexts into a single shared
// gocui view (many-contexts-ONE-view topology, mirroring SchemaRailContext)
// — an editable editor leaf alongside stateless list leaves. The container
// is the only renderer of its view; the leaves carry GetViewName() resolving
// to the same view and render only when the container calls the active leaf
// directly. The leaves are never pushed onto the focus stack; switching tabs
// mutates the active index, fires the outgoing/incoming leaf focus hooks, and
// re-renders.
//
// Unlike SchemaRailContext (which has two homogeneous SideListContext
// leaves), QueryRailContext holds leaves by the IBaseContext interface so it
// can host the editor leaf and list leaves uniformly. The build is concrete
// (no shared generic abstraction) per CLAUDE.md "no abstractions for
// single-use code"; reuse with the schema rail is deferred.
//
// Per-tab scroll origin is stored here so a horizontal pan on one tab does
// not bleed into another through the shared view. CRITICAL: the editor leaf
// OPTS OUT of the generic origin save/restore (its tab carries
// managesOwnOrigin) — the editor drives its own scroll every frame
// (layout.go FocusPoint + scrollEditorColumnIntoView), and restoring a saved
// origin would fight that, pinning the view to a stale origin.
type QueryRailContext struct {
	BaseContext

	deps Deps

	tabs      []queryRailTab
	activeTab int

	// log carries the session logger so SetActiveTab can emit the
	// tab_switch event. Nil-safe: logs.Event guards a nil logger, so a
	// container constructed without SetLogger simply emits nothing.
	log *slog.Logger

	// restorePending is set by SetActiveTab and consumed by the next
	// HandleRender so the incoming tab's saved origin is re-applied exactly
	// ONCE, on the switch frame. Restoring every frame would clobber a
	// leaf's deferred scroll-to-cursor and live horizontal pan, pinning the
	// view to the stale per-tab origin so it never follows the cursor. The
	// editor tab (managesOwnOrigin) skips restore entirely.
	restorePending bool
}

// queryRailTab is one tab in the container: the human label, the leaf's
// stable key (for ActiveLeafKey + the tab_switch event), the live leaf
// reference (injected by SetLeaves), the saved (ox, oy) view origin, and
// the managesOwnOrigin opt-out flag for the editor leaf.
type queryRailTab struct {
	label            string
	leafKey          types.ContextKey
	leaf             types.IBaseContext
	origin           [2]int
	managesOwnOrigin bool
}

// QueryRailTabSpec declares a tab at construction time. The leaf reference
// is injected later via SetLeaves (the spec build order constructs the
// leaves before the container). ManagesOwnOrigin must be set for the editor
// leaf so the container's origin save/restore machinery skips it.
type QueryRailTabSpec struct {
	Label            string
	LeafKey          types.ContextKey
	ManagesOwnOrigin bool
}

// NewQueryRailContext builds the container with the supplied tab specs. The
// active tab defaults to 0. Leaf references are injected via SetLeaves once
// all contexts are constructed.
func NewQueryRailContext(base BaseContext, deps Deps, specs ...QueryRailTabSpec) *QueryRailContext {
	tabs := make([]queryRailTab, len(specs))
	for i, s := range specs {
		tabs[i] = queryRailTab{
			label:            s.Label,
			leafKey:          s.LeafKey,
			managesOwnOrigin: s.ManagesOwnOrigin,
		}
	}
	return &QueryRailContext{
		BaseContext: base,
		deps:        deps,
		tabs:        tabs,
	}
}

// SetLeaves injects the live leaf contexts positionally, matching the tab
// spec order passed to NewQueryRailContext. Called once at wiring time.
// Extra leaves beyond the declared tab count are ignored.
func (q *QueryRailContext) SetLeaves(leaves ...types.IBaseContext) {
	for i := range q.tabs {
		if i >= len(leaves) {
			return
		}
		q.tabs[i].leaf = leaves[i]
	}
}

// SetLogger injects the session logger used for the tab_switch event.
// Nil-safe everywhere it is read.
func (q *QueryRailContext) SetLogger(log *slog.Logger) { q.log = log }

// dirtyFlusher is the minimal flush seam the editor leaf exposes
// (QueryEditorContext.FlushDirty) so the container can flush unsaved edits
// on focus loss without depending on the concrete editor type.
type dirtyFlusher interface {
	FlushDirty() error
}

// HandleFocus delegates to the active leaf's HandleFocus so the incoming
// tab runs its own focus protocol (e.g. the editor leaf flips its mode to
// Normal). The container itself holds no focus state. Nil-safe on an
// unwired leaf.
func (q *QueryRailContext) HandleFocus(opts types.OnFocusOpts) error {
	if len(q.tabs) == 0 {
		return nil
	}
	leaf := q.tabs[q.activeTab].leaf
	if leaf == nil {
		return nil
	}
	return leaf.HandleFocus(opts)
}

// HandleFocusLost delegates to the active leaf's HandleFocusLost AND
// UNCONDITIONALLY flushes the editor leaf's dirty buffer — even when a list
// tab (SAVED_QUERY / HISTORY) is active. The container leaves the focus
// stack as a single MAIN_CONTEXT, so a MAIN_CONTEXT push (opening the
// connection manager → removeMain) fires this once; without the
// unconditional flush, edits typed in the editor and then left via a list
// tab would be silently dropped. The flush uses the editor leaf's own save
// path (FlushDirty); a no-op when the buffer is clean.
func (q *QueryRailContext) HandleFocusLost(opts types.OnFocusLostOpts) error {
	if len(q.tabs) == 0 {
		return nil
	}
	if leaf := q.tabs[q.activeTab].leaf; leaf != nil {
		_ = leaf.HandleFocusLost(opts)
	}
	return q.flushEditorLeaf()
}

// flushEditorLeaf flushes every leaf that exposes the dirtyFlusher seam
// (the editor leaf). The active-tab delegation above already runs the
// editor's own HandleFocusLost when the editor IS active, so to avoid
// double-flushing only flush leaves OTHER than the active one here.
func (q *QueryRailContext) flushEditorLeaf() error {
	for i, t := range q.tabs {
		if i == q.activeTab {
			continue
		}
		if f, ok := t.leaf.(dirtyFlusher); ok {
			_ = f.FlushDirty()
		}
	}
	return nil
}

// ActiveTab returns the current active-tab index.
func (q *QueryRailContext) ActiveTab() int { return q.activeTab }

// ActiveLeafKey returns the active leaf's ContextKey, or the empty key when
// there are no tabs.
func (q *QueryRailContext) ActiveLeafKey() types.ContextKey {
	if len(q.tabs) == 0 {
		return ""
	}
	return q.tabs[q.activeTab].leafKey
}

// SetActiveTab switches the active tab, clamping idx into [0, len-1]. A
// no-op switch (idx == activeTab) leaves the saved origins and focus hooks
// untouched — no hooks fire. On a REAL switch it fires the OUTGOING leaf's
// HandleFocusLost then the INCOMING leaf's HandleFocus, each exactly once,
// passing ZERO-VALUE OnFocusLostOpts / OnFocusOpts (the container has no
// click/key provenance to forward at this layer), captures the outgoing
// tab's view origin, and emits a tab_switch event (metadata only — never SQL
// or buffer text).
func (q *QueryRailContext) SetActiveTab(idx int) {
	if len(q.tabs) == 0 {
		return
	}
	idx = q.clampTab(idx)
	if idx == q.activeTab {
		return
	}

	from := q.activeTab
	q.saveActiveOrigin()
	q.fireLeafFocusLost(from)
	q.activeTab = idx
	q.fireLeafFocus(idx)
	q.restorePending = true
	q.logTabSwitch(from, idx)
}

// fireLeafFocusLost invokes the outgoing leaf's HandleFocusLost once with
// zero-value opts. Nil-safe when the leaf is unwired.
func (q *QueryRailContext) fireLeafFocusLost(idx int) {
	leaf := q.tabs[idx].leaf
	if leaf == nil {
		return
	}
	_ = leaf.HandleFocusLost(types.OnFocusLostOpts{})
}

// fireLeafFocus invokes the incoming leaf's HandleFocus once with zero-value
// opts. The leaf's focus path must be cursor-PRESERVING (it must not
// unconditionally reset the cursor) so a re-focus does not stomp a
// user-moved cursor. Nil-safe when the leaf is unwired.
func (q *QueryRailContext) fireLeafFocus(idx int) {
	leaf := q.tabs[idx].leaf
	if leaf == nil {
		return
	}
	_ = leaf.HandleFocus(types.OnFocusOpts{})
}

// logTabSwitch emits the tab_switch event mirroring the ctx_replace shape:
// METADATA ONLY (from/to leaf keys + the active index), never SQL or buffer
// text. Nil-safe via logs.Event.
func (q *QueryRailContext) logTabSwitch(from, to int) {
	logs.Event(q.log, "input", "tab_switch",
		slog.String("from_leaf", string(q.tabs[from].leafKey)),
		slog.String("to_leaf", string(q.tabs[to].leafKey)),
		slog.Int("active", to),
	)
}

// HandleRender publishes the tab strip every frame (so gocui's drawTitle
// keeps preferring the live Tabs over the stale v.Title the layout loop
// sets) with the active label marked, restores the incoming tab's saved
// origin once on the switch frame, and renders ONLY the active leaf into the
// shared view. A nil/unwired active leaf is a no-op render that never panics.
func (q *QueryRailContext) HandleRender() error {
	if len(q.tabs) == 0 {
		return nil
	}
	if q.deps.GuiDriver != nil {
		_ = q.deps.GuiDriver.SetViewTabs(q.GetViewName(), q.tabLabels(), q.activeTab)
	}
	if q.restorePending {
		q.restoreActiveOrigin()
		q.restorePending = false
	}
	leaf := q.tabs[q.activeTab].leaf
	if leaf == nil {
		return nil
	}
	return leaf.HandleRender()
}

// tabLabels returns the per-frame label slice with the active label marked.
// Allocated per call (the tab count is tiny); kept simple over the schema
// rail's hoisted lookup table since the labels are instance data here, not
// package constants.
func (q *QueryRailContext) tabLabels() []string {
	labels := make([]string, len(q.tabs))
	for i, t := range q.tabs {
		labels[i] = t.label
		if i == q.activeTab {
			labels[i] = markActiveTab(t.label)
		}
	}
	return labels
}

// saveActiveOrigin captures the shared view's current origin into the active
// tab's slot. No-op for a managesOwnOrigin tab (the editor), without a
// driver, or without a realised view.
func (q *QueryRailContext) saveActiveOrigin() {
	if q.tabs[q.activeTab].managesOwnOrigin {
		return
	}
	if q.deps.GuiDriver == nil {
		return
	}
	v, err := q.deps.GuiDriver.ViewByName(q.GetViewName())
	if err != nil || v == nil {
		return
	}
	ox, oy := v.Origin()
	q.tabs[q.activeTab].origin = [2]int{ox, oy}
}

// restoreActiveOrigin re-applies the active tab's saved origin to the shared
// view. Called ONLY on the switch frame (guarded by restorePending). No-op
// for a managesOwnOrigin tab (the editor drives its own scroll), without a
// driver, or without a realised view.
func (q *QueryRailContext) restoreActiveOrigin() {
	if q.tabs[q.activeTab].managesOwnOrigin {
		return
	}
	if q.deps.GuiDriver == nil {
		return
	}
	saved := q.tabs[q.activeTab].origin
	v, err := q.deps.GuiDriver.ViewByName(q.GetViewName())
	if err != nil || v == nil {
		return
	}
	v.SetOrigin(saved[0], saved[1])
}

// clampTab clamps an arbitrary index into [0, len(tabs)-1].
func (q *QueryRailContext) clampTab(idx int) int {
	if idx < 0 {
		return 0
	}
	if idx > len(q.tabs)-1 {
		return len(q.tabs) - 1
	}
	return idx
}
