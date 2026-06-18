package context

import "github.com/davesavic/pgsavvy/pkg/gui/types"

// TableInspectViewName is the single gocui view the TABLE_INSPECT container and
// both leaves (COLUMNS, INDEXES) render into (many-contexts-ONE-view topology).
// It MUST be set on all three specs (container + both leaves) in setup.go so the
// leaf SetContent + the container SetViewTabs target the SAME view the layout
// creates for the popup — otherwise the popup renders blank. The value matches
// the historical string(types.TABLE_INSPECT) so existing layout/view wiring is
// unchanged.
const TableInspectViewName = "table_inspect"

// TableInspectContext is the TEMPORARY_POPUP container that multiplexes the
// COLUMNS and INDEXES leaves into the single "table_inspect" view
// (many-contexts-ONE-view topology). It is a THIN ADAPTER over the shared
// TabbedRailContext core: all tabbed-pane mechanics (tab switching, tab-strip
// publishing) live in the embedded core; this type adds the loading gate and
// per-tab scroll the layout pass clamps via applyTableInspectScroll.
//
// The container constructs the core with FireFocusHooks=FALSE (both leaves live
// under the single TABLE_INSPECT scope, so a tab switch is not a focus
// transition) and ManagesOwnOrigin=TRUE on both tabs (this context owns the
// per-tab scroll, and applyTableInspectScroll in layout.go is the single origin
// owner — see the TabSpecs in NewTableInspectContext).
//
// Unlike ExportMenuContext, this context does NOT gate on an Active()
// predicate. TEMPORARY_POPUP rendering is already gated by focus-stack
// membership in pkg/gui/orchestrator/layout.go — when the popup isn't on the
// stack, HandleRender is not invoked.
type TableInspectContext struct {
	*TabbedRailContext

	deps Deps

	loading bool
	schema  string
	table   string

	// scroll holds the per-tab (x, y) view origin offsets (columns / lines),
	// sized to the tab count and indexed by the embedded core's ActiveTab().
	// This context owns only the top/left clamp (>= 0); the layout pass
	// (applyTableInspectScroll, layout.go) owns the bottom/right clamp against
	// the rendered content extent (it alone knows LinesHeight / max line width
	// vs the viewport) and is the single writer of the gocui view origin.
	scroll [][2]int
}

// NewTableInspectContext builds a TableInspectContext bound to TABLE_INSPECT as
// a thin adapter over a TabbedRailContext core with exactly two tabs (Columns,
// Indexes). Leaf references are injected via the promoted SetLeaves at wiring
// time (setup.go). The active tab defaults to Columns (0).
func NewTableInspectContext(base BaseContext, deps Deps) *TableInspectContext {
	core := NewTabbedRailContext(base, deps, TabbedRailOpts{
		// FireFocusHooks=false: both leaves share the single TABLE_INSPECT
		// scope, so a tab switch is NOT a focus transition — firing per-leaf
		// focus hooks would be spurious. The leaves are stateless list renderers.
		FireFocusHooks: false,
	},
		// ManagesOwnOrigin=true on BOTH tabs: this context owns the per-tab
		// scroll and applyTableInspectScroll (layout.go) is the single writer of
		// the view origin, so the core's generic per-tab origin save/restore is
		// disabled. (It would be dead anyway: the view is recreated on every
		// open because TABLE_INSPECT is a TEMPORARY_POPUP, so a saved tab.origin
		// never survives to be restored.)
		TabSpec{Label: "Columns", LeafKey: types.COLUMNS, ManagesOwnOrigin: true},
		TabSpec{Label: "Indexes", LeafKey: types.INDEXES, ManagesOwnOrigin: true},
	)
	return &TableInspectContext{
		TabbedRailContext: core,
		deps:              deps,
		scroll:            make([][2]int, core.TabCount()),
	}
}

// HandleRender writes "Loading…" into the view while a columns/indexes fetch is
// in flight, otherwise delegates to the embedded core (which publishes the tab
// strip and renders the active leaf). Panels SafeText DB-supplied leaves
// (AD-17); the container does not re-strip the composed body.
func (c *TableInspectContext) HandleRender() error {
	if c.loading {
		viewName := c.GetViewName()
		writeView(c.deps, func() error {
			return c.deps.GuiDriver.SetContent(viewName, "Loading…")
		})
		return nil
	}
	return c.TabbedRailContext.HandleRender()
}

// activeScroll returns a pointer to the active tab's stored (x, y) pair, or nil
// when the scroll store is empty / the active index is out of range. Guards the
// scroll accessors against an unsized store.
func (c *TableInspectContext) activeScroll() *[2]int {
	idx := c.ActiveTab()
	if idx < 0 || idx >= len(c.scroll) {
		return nil
	}
	return &c.scroll[idx]
}

// Scroll moves the ACTIVE tab's view origin by (dx, dy) — columns and lines —
// clamping at the top-left edge (never negative). The bottom/right bound is
// enforced by the layout pass, which knows the rendered content extent.
func (c *TableInspectContext) Scroll(dx, dy int) {
	c.SetScrollX(c.ScrollX() + dx)
	c.SetScrollY(c.ScrollY() + dy)
}

// SetScrollX sets the ACTIVE tab's absolute horizontal origin, clamping at the
// left. No-op when the scroll store is unsized.
func (c *TableInspectContext) SetScrollX(x int) {
	s := c.activeScroll()
	if s == nil {
		return
	}
	if x < 0 {
		x = 0
	}
	s[0] = x
}

// SetScrollY sets the ACTIVE tab's absolute vertical origin, clamping at the
// top. The layout pass calls this to write back the value it clamped to the
// content's last page (so `G` / over-scroll settle on the last page). No-op when
// the scroll store is unsized.
func (c *TableInspectContext) SetScrollY(y int) {
	s := c.activeScroll()
	if s == nil {
		return
	}
	if y < 0 {
		y = 0
	}
	s[1] = y
}

// ScrollX returns the ACTIVE tab's horizontal origin (columns). Zero when the
// scroll store is unsized.
func (c *TableInspectContext) ScrollX() int {
	if s := c.activeScroll(); s != nil {
		return s[0]
	}
	return 0
}

// ScrollY returns the ACTIVE tab's vertical origin (lines). Zero when the
// scroll store is unsized.
func (c *TableInspectContext) ScrollY() int {
	if s := c.activeScroll(); s != nil {
		return s[1]
	}
	return 0
}

// SetLoading toggles the loading indicator. When true, HandleRender emits a
// "Loading…" body instead of delegating to the core.
func (c *TableInspectContext) SetLoading(v bool) { c.loading = v }

// IsLoading reports the current loading flag.
func (c *TableInspectContext) IsLoading() bool { return c.loading }

// Target returns the schema/table the popup is currently inspecting. Zero
// values when unset.
func (c *TableInspectContext) Target() (schema, table string) {
	return c.schema, c.table
}

// SetTarget records the schema/table the popup is inspecting and resets every
// tab's stored scroll to (0, 0) so a re-open on a new table lands top-left on
// both tabs. Pure state; callers are responsible for triggering refetches.
func (c *TableInspectContext) SetTarget(schema, table string) {
	c.schema = schema
	c.table = table
	for i := range c.scroll {
		c.scroll[i] = [2]int{0, 0}
	}
}
