package context

import (
	"strconv"
	"strings"

	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

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

	// tbl is the live target table reference (set per open via SetStats). Its
	// atomic counters are read LIVE in StatsLine each frame so the subtitle
	// reflects the async stats enrichment after the popup opens. nil until the
	// first SetStats; reset on every reopen so a stale prior table never shows.
	tbl *models.Table

	// scroll holds the per-tab (x, y) view origin offsets (columns / lines),
	// sized to the tab count and indexed by the embedded core's ActiveTab().
	// This context owns only the top/left clamp (>= 0); the layout pass
	// (applyTableInspectScroll, layout.go) owns the bottom/right clamp against
	// the rendered content extent (it alone knows LinesHeight / max line width
	// vs the viewport) and is the single writer of the gocui view origin.
	scroll [][2]int
}

// NewTableInspectContext builds a TableInspectContext bound to TABLE_INSPECT as
// a thin adapter over a TabbedRailContext core with four tabs (Columns,
// Indexes, Foreign Keys, Constraints). Leaf references are injected via the
// promoted SetLeaves at wiring time (setup.go). The active tab defaults to
// Columns (0).
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
		TabSpec{Label: "Foreign Keys", LeafKey: types.FOREIGN_KEYS, ManagesOwnOrigin: true},
		TabSpec{Label: "Constraints", LeafKey: types.CONSTRAINTS, ManagesOwnOrigin: true},
	)
	c := &TableInspectContext{
		TabbedRailContext: core,
		deps:              deps,
		scroll:            make([][2]int, core.TabCount()),
	}
	// Pin the table's stats line as the first body line above the active leaf.
	// The 4-tab top border (Columns/Indexes/Foreign Keys/Constraints) leaves no
	// room for a right-aligned subtitle, so the stats live in the body instead.
	// bodyHeaderLine returns "" until a target is set, so the hook is inert for
	// the unwired/no-target case (leaf-only render).
	core.SetBodyHeader(c.bodyHeaderLine)
	return c
}

// bodyHeaderLine is the stats provider fed to the core's body-header hook. It
// returns "" before a target is set so the core renders leaf-only; once a
// target exists it returns StatsLine ("schema.table" plus "~N rows · size" once
// the async stats land).
func (c *TableInspectContext) bodyHeaderLine() string {
	if c.schema == "" && c.table == "" {
		return ""
	}
	return c.StatsLine()
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

// SetStats records the live target table reference whose atomic stats counters
// StatsLine reads each frame. Stored as a reference (NOT copied ints) because
// EstimatedRows/SizeBytes enrich ASYNC after the popup opens; a captured int
// snapshot would never reflect the later fill. Called on every open so a stale
// prior table is never shown.
func (c *TableInspectContext) SetStats(tbl *models.Table) { c.tbl = tbl }

// StatsLine composes the popup's top-border subtitle: "schema.table" always,
// optionally followed by "  ~<N> rows · <size>". Stats are read LIVE via the
// atomic counters' Load() each frame (see SetStats). The stats segment is
// suppressed (schema.table only) when rows < 0 (never-analyzed reltuples=-1) or
// rows == 0 && bytes == 0 (async fill not yet landed) so the popup never renders
// the "~0 rows · 0 B" lie.
func (c *TableInspectContext) StatsLine() string {
	base := config.SafeText(c.schema) + "." + config.SafeText(c.table)
	if c.tbl == nil {
		return base
	}
	rows := c.tbl.EstimatedRows.Load()
	bytes := c.tbl.SizeBytes.Load()
	if rows < 0 || (rows == 0 && bytes == 0) {
		return base
	}
	return base + "  ~" + humanizeRows(rows) + " rows · " + bytesHuman(bytes)
}

// humanizeRows renders a row estimate compactly: < 1000 exact, >= 1000 -> "1.2k",
// >= 1e6 -> "1.2M". Negatives clamp to "0". Mirrors
// controllers.humanizeEstimate (reimplemented here to avoid a cross-package
// import).
func humanizeRows(n int64) string {
	if n < 1000 {
		if n < 0 {
			return "0"
		}
		return strconv.FormatInt(n, 10)
	}
	if n < 1_000_000 {
		return trimRows(float64(n)/1000) + "k"
	}
	return trimRows(float64(n)/1_000_000) + "M"
}

// trimRows renders v to one decimal place, dropping a trailing ".0" so
// 1200 -> "1.2", 12000 -> "12".
func trimRows(v float64) string {
	return strings.TrimSuffix(strconv.FormatFloat(v, 'f', 1, 64), ".0")
}

// bytesHuman renders a byte count base-1024: bytes < 1024 as exact "N B" (no
// decimal); KB and up with exactly one decimal place (B/KB/MB/GB/TB).
func bytesHuman(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10) + " B"
	}
	units := []string{"KB", "MB", "GB", "TB"}
	v := float64(n) / 1024
	for _, u := range units {
		if v < 1024 || u == "TB" {
			return strconv.FormatFloat(v, 'f', 1, 64) + " " + u
		}
		v /= 1024
	}
	return strconv.FormatFloat(v, 'f', 1, 64) + " TB"
}
