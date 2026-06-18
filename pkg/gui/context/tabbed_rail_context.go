package context

import (
	"log/slog"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/logs"
)

// TabbedRailContext is the shared core for the "many-contexts-ONE-view tabbed
// pane" pattern: a container that multiplexes a set of HETEROGENEOUS leaf
// contexts into a single shared gocui view. The container is the only renderer
// of its view; leaves carry GetViewName() resolving to the same view and render
// only when the container calls the active leaf directly. The leaves are never
// pushed onto the focus stack; switching tabs mutates the active index,
// optionally fires the outgoing/incoming leaf focus hooks, and re-renders.
//
// It generalizes QueryRailContext (which is the near-general form of the
// pattern already): leaves are held by the IBaseContext interface so the
// container can host an editable editor leaf alongside stateless list leaves
// uniformly, and per-tab scroll origin is stored here so a horizontal pan on
// one tab does not bleed into another through the shared view. A tab may OPT
// OUT of the generic origin save/restore (its tab carries managesOwnOrigin) —
// the query-editor leaf drives its own scroll every frame, and restoring a
// saved origin would fight that, pinning the view to a stale origin.
//
// Concurrency: N/A. Every method runs on the single gocui MainLoop (UI thread).
type TabbedRailContext struct {
	BaseContext

	deps Deps

	tabs      []tab
	activeTab int

	// fireFocusHooks gates ONLY the SetActiveTab tab-switch leaf hooks (the
	// outgoing leaf HandleFocusLost + incoming leaf HandleFocus). The
	// container's own HandleFocus / HandleFocusLost ALWAYS delegate/flush
	// regardless of this flag. See TabbedRailOpts.FireFocusHooks for WHY this
	// differs per consumer.
	fireFocusHooks bool

	// log carries the session logger so SetActiveTab can emit the tab_switch
	// event. Nil-safe: logs.Event guards a nil logger, so a container
	// constructed without SetLogger simply emits nothing.
	log *slog.Logger

	// restorePending is set by SetActiveTab and consumed by the next
	// HandleRender so the incoming tab's saved origin is re-applied exactly
	// ONCE, on the switch frame. Restoring every frame would clobber a leaf's
	// deferred scroll-to-cursor and live horizontal pan, pinning the view to
	// the stale per-tab origin so it never follows the cursor. A
	// managesOwnOrigin tab skips restore entirely.
	restorePending bool
}

// tab is one tab in the container: the human label, the leaf's stable key
// (for ActiveLeafKey + the tab_switch event), the live leaf reference
// (injected by SetLeaves), the saved (ox, oy) view origin, and the
// managesOwnOrigin opt-out flag for self-scrolling leaves. The leaf is held as
// IBaseContext so the container can host heterogeneous leaves.
type tab struct {
	label            string
	leafKey          types.ContextKey
	leaf             types.IBaseContext
	origin           [2]int
	managesOwnOrigin bool
}

// TabSpec declares a tab at construction time. The leaf reference is injected
// later via SetLeaves (the spec build order constructs the leaves before the
// container). ManagesOwnOrigin must be set for a self-scrolling leaf (e.g. the
// query editor) so the container's origin save/restore machinery skips it.
type TabSpec struct {
	Label            string
	LeafKey          types.ContextKey
	ManagesOwnOrigin bool
}

// TabbedRailOpts configures container-wide behaviour at construction time.
type TabbedRailOpts struct {
	// FireFocusHooks controls whether a SetActiveTab tab switch fires the
	// outgoing leaf's HandleFocusLost and the incoming leaf's HandleFocus.
	//
	// This MUST differ per consumer because it depends on the master-editor
	// scope topology, NOT on the tabbed-pane mechanics:
	//
	//   - The QUERY_RAIL sets this TRUE: its leaves (editor, history, saved)
	//     live under DISTINCT master-editor scopes, so a tab switch is a real
	//     focus transition between scopes and each leaf must run its own focus
	//     protocol (e.g. the editor leaf flipping its mode to Normal).
	//
	//   - The SCHEMA_RAIL sets this FALSE: both leaves (schemas, tables) share
	//     ONE SCHEMA_RAIL scope, so a tab switch is not a focus transition —
	//     firing per-leaf focus hooks would be spurious.
	FireFocusHooks bool
}

// NewTabbedRailContext builds the container with the supplied tab specs. The
// active tab defaults to 0. Leaf references are injected via SetLeaves once all
// contexts are constructed.
func NewTabbedRailContext(base BaseContext, deps Deps, opts TabbedRailOpts, specs ...TabSpec) *TabbedRailContext {
	tabs := make([]tab, len(specs))
	for i, s := range specs {
		tabs[i] = tab{
			label:            s.Label,
			leafKey:          s.LeafKey,
			managesOwnOrigin: s.ManagesOwnOrigin,
		}
	}
	return &TabbedRailContext{
		BaseContext:    base,
		deps:           deps,
		tabs:           tabs,
		fireFocusHooks: opts.FireFocusHooks,
	}
}

// SetLeaves injects the live leaf contexts positionally, matching the tab spec
// order passed to NewTabbedRailContext. Called once at wiring time. Extra
// leaves beyond the declared tab count are ignored.
func (t *TabbedRailContext) SetLeaves(leaves ...types.IBaseContext) {
	for i := range t.tabs {
		if i >= len(leaves) {
			return
		}
		t.tabs[i].leaf = leaves[i]
	}
}

// SetLogger injects the session logger used for the tab_switch event.
// Nil-safe everywhere it is read.
func (t *TabbedRailContext) SetLogger(log *slog.Logger) { t.log = log }

// HandleFocus delegates to the active leaf's HandleFocus so the incoming
// container runs the active leaf's focus protocol. ALWAYS delegates —
// independent of FireFocusHooks (which gates only the SetActiveTab tab-switch
// hooks). Nil-safe on an unwired leaf or empty container.
func (t *TabbedRailContext) HandleFocus(opts types.OnFocusOpts) error {
	leaf := t.activeLeaf()
	if leaf == nil {
		return nil
	}
	return leaf.HandleFocus(opts)
}

// HandleFocusLost delegates to the active leaf's HandleFocusLost AND flushes
// every NON-active leaf that exposes the dirtyFlusher seam (folding QueryRail's
// unconditional editor-flush into the core). The active index is skipped so the
// active leaf is not double-flushed (its own HandleFocusLost already ran). This
// flush ALWAYS happens — it is NOT gated by FireFocusHooks. Nil-safe on an
// unwired leaf or empty container.
func (t *TabbedRailContext) HandleFocusLost(opts types.OnFocusLostOpts) error {
	if leaf := t.activeLeaf(); leaf != nil {
		_ = leaf.HandleFocusLost(opts)
	}
	t.flushInactiveDirty()
	return nil
}

// flushInactiveDirty flushes every NON-active leaf that exposes the
// dirtyFlusher seam. Skipping the active index avoids double-flushing the leaf
// whose own HandleFocusLost ran in HandleFocusLost above.
func (t *TabbedRailContext) flushInactiveDirty() {
	for i, tb := range t.tabs {
		if i == t.activeTab {
			continue
		}
		if f, ok := tb.leaf.(dirtyFlusher); ok {
			_ = f.FlushDirty()
		}
	}
}

// ActiveTab returns the current active-tab index.
func (t *TabbedRailContext) ActiveTab() int { return t.activeTab }

// ActiveLeafKey returns the active leaf's ContextKey, or the empty key when
// there are no tabs.
func (t *TabbedRailContext) ActiveLeafKey() types.ContextKey {
	if len(t.tabs) == 0 {
		return ""
	}
	return t.tabs[t.activeTab].leafKey
}

// activeLeaf returns the active leaf as its IBaseContext, or nil when the
// container is empty or the leaf is unwired. Interface dispatch reaches the
// concrete leaf's HandleRender / focus overrides.
func (t *TabbedRailContext) activeLeaf() types.IBaseContext {
	if len(t.tabs) == 0 {
		return nil
	}
	return t.tabs[t.activeTab].leaf
}

// SetActiveTab switches the active tab, clamping idx into [0, len-1]. A no-op
// switch (idx == activeTab) leaves the saved origins and focus hooks untouched
// — no hooks fire, no origin is saved.
//
// On a REAL switch the order is critical and MUST be:
//  1. save the OUTGOING tab's origin (skipped when it managesOwnOrigin),
//  2. (when FireFocusHooks) fire the OUTGOING leaf's HandleFocusLost,
//  3. set the new activeTab,
//  4. (when FireFocusHooks) fire the INCOMING leaf's HandleFocus.
//
// The leaf hooks bracket the activeTab mutation so the outgoing leaf tears down
// before the incoming leaf sets up — running HandleFocus before activeTab moved
// (or HandleFocusLost after) would target the wrong leaf. The opts passed are
// ZERO-VALUE (the container has no click/key provenance to forward at this
// layer). Emits a tab_switch event (metadata only — never SQL or buffer text).
func (t *TabbedRailContext) SetActiveTab(idx int) {
	if len(t.tabs) == 0 {
		return
	}
	idx = t.clampTab(idx)
	if idx == t.activeTab {
		return
	}

	from := t.activeTab
	t.saveActiveOrigin()
	if t.fireFocusHooks {
		t.fireLeafFocusLost(from)
	}
	t.activeTab = idx
	if t.fireFocusHooks {
		t.fireLeafFocus(idx)
	}
	t.restorePending = true
	t.logTabSwitch(from, idx)
}

// fireLeafFocusLost invokes the outgoing leaf's HandleFocusLost once with
// zero-value opts. Nil-safe when the leaf is unwired.
func (t *TabbedRailContext) fireLeafFocusLost(idx int) {
	leaf := t.tabs[idx].leaf
	if leaf == nil {
		return
	}
	_ = leaf.HandleFocusLost(types.OnFocusLostOpts{})
}

// fireLeafFocus invokes the incoming leaf's HandleFocus once with zero-value
// opts. The leaf's focus path must be cursor-PRESERVING so a re-focus does not
// stomp a user-moved cursor. Nil-safe when the leaf is unwired.
func (t *TabbedRailContext) fireLeafFocus(idx int) {
	leaf := t.tabs[idx].leaf
	if leaf == nil {
		return
	}
	_ = leaf.HandleFocus(types.OnFocusOpts{})
}

// logTabSwitch emits the tab_switch event: METADATA ONLY (from/to leaf keys +
// the active index), never SQL or buffer text. Nil-safe via logs.Event.
func (t *TabbedRailContext) logTabSwitch(from, to int) {
	logs.Event(t.log, "input", "tab_switch",
		slog.String("from_leaf", string(t.tabs[from].leafKey)),
		slog.String("to_leaf", string(t.tabs[to].leafKey)),
		slog.Int("active", to),
	)
}

// HandleRender publishes the tab strip every frame (so gocui's drawTitle keeps
// preferring the live Tabs over the stale v.Title the layout loop sets) with
// the active label marked, restores the incoming tab's saved origin once on the
// switch frame, and renders ONLY the active leaf into the shared view. A
// nil/unwired active leaf (or empty container) is a no-op render that never
// panics.
func (t *TabbedRailContext) HandleRender() error {
	if len(t.tabs) == 0 {
		return nil
	}
	if t.deps.GuiDriver != nil {
		_ = t.deps.GuiDriver.SetViewTabs(t.GetViewName(), t.tabLabels(), t.activeTab)
	}
	if t.restorePending {
		t.restoreActiveOrigin()
		t.restorePending = false
	}
	leaf := t.tabs[t.activeTab].leaf
	if leaf == nil {
		return nil
	}
	return leaf.HandleRender()
}

// markActiveTab brackets a label so the active tab reads e.g. "[Schemas]".
// Brackets work in NO_COLOR / mono and regardless of focus.
func markActiveTab(label string) string { return "[" + label + "]" }

// tabLabels returns the per-frame label slice with the active label marked.
// Allocated per call (the tab count is tiny).
func (t *TabbedRailContext) tabLabels() []string {
	labels := make([]string, len(t.tabs))
	for i, tb := range t.tabs {
		labels[i] = tb.label
		if i == t.activeTab {
			labels[i] = markActiveTab(tb.label)
		}
	}
	return labels
}

// saveActiveOrigin captures the shared view's current origin into the active
// tab's slot. No-op for a managesOwnOrigin tab, without a driver, or without a
// realised view.
func (t *TabbedRailContext) saveActiveOrigin() {
	if t.tabs[t.activeTab].managesOwnOrigin {
		return
	}
	if t.deps.GuiDriver == nil {
		return
	}
	v, err := t.deps.GuiDriver.ViewByName(t.GetViewName())
	if err != nil || v == nil {
		return
	}
	ox, oy := v.Origin()
	t.tabs[t.activeTab].origin = [2]int{ox, oy}
}

// restoreActiveOrigin re-applies the active tab's saved origin to the shared
// view. Called ONLY on the switch frame (guarded by restorePending). No-op for
// a managesOwnOrigin tab (it drives its own scroll), without a driver, or
// without a realised view.
func (t *TabbedRailContext) restoreActiveOrigin() {
	if t.tabs[t.activeTab].managesOwnOrigin {
		return
	}
	if t.deps.GuiDriver == nil {
		return
	}
	saved := t.tabs[t.activeTab].origin
	v, err := t.deps.GuiDriver.ViewByName(t.GetViewName())
	if err != nil || v == nil {
		return
	}
	v.SetOrigin(saved[0], saved[1])
}

// clampTab clamps an arbitrary index into [0, len(tabs)-1].
func (t *TabbedRailContext) clampTab(idx int) int {
	if idx < 0 {
		return 0
	}
	if idx > len(t.tabs)-1 {
		return len(t.tabs) - 1
	}
	return idx
}
