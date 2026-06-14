package context

import (
	"github.com/davesavic/dbsavvy/pkg/gui/popup"
)

// TableInspectContext renders the tabbed columns/indexes inspect popup
// popup. State is owned by a *popup.TabbedPopup installed
// via SetState; HandleRender writes its Body() into the gocui view.
//
// Unlike ExportMenuContext, this context does NOT gate on an Active()
// predicate. TEMPORARY_POPUP rendering is already gated by focus-stack
// membership in pkg/gui/orchestrator/layout.go — when the popup isn't
// on the stack, HandleRender is not invoked. Loading state is exposed
// so the open action (T9) can surface a "Loading…" body while
// columns/indexes fetches are in flight.
type TableInspectContext struct {
	BaseContext

	deps Deps

	state   *popup.TabbedPopup
	loading bool
	schema  string
	table   string

	// scrollX / scrollY are the view origin offsets (columns / lines).
	// The context owns only the top/left clamp (>= 0); the layout pass
	// owns the bottom/right clamp against the rendered content extent
	// (it alone knows LinesHeight / max line width vs the viewport).
	scrollX int
	scrollY int
}

// NewTableInspectContext builds a TableInspectContext bound to TABLE_INSPECT.
func NewTableInspectContext(base BaseContext, deps Deps) *TableInspectContext {
	return &TableInspectContext{BaseContext: base, deps: deps}
}

// SetState installs the TabbedPopup that supplies the rendered body.
// Nil is permitted: HandleRender emits an empty body when state is unset
// and the context is not loading. Installing fresh state resets the
// scroll origin so the popup opens at the top-left.
func (c *TableInspectContext) SetState(s *popup.TabbedPopup) {
	c.state = s
	c.scrollX = 0
	c.scrollY = 0
}

// Scroll moves the view origin by (dx, dy) — columns and lines — clamping
// at the top-left edge (never negative). The bottom/right bound is
// enforced by the layout pass, which knows the rendered content extent.
func (c *TableInspectContext) Scroll(dx, dy int) {
	c.SetScrollX(c.scrollX + dx)
	c.SetScrollY(c.scrollY + dy)
}

// SetScrollX sets the absolute horizontal origin, clamping at the left.
func (c *TableInspectContext) SetScrollX(x int) {
	if x < 0 {
		x = 0
	}
	c.scrollX = x
}

// SetScrollY sets the absolute vertical origin, clamping at the top. The
// layout pass calls this to write back the value it clamped to the
// content's last page (so `G` / over-scroll settle on the last page).
func (c *TableInspectContext) SetScrollY(y int) {
	if y < 0 {
		y = 0
	}
	c.scrollY = y
}

// ScrollX returns the current horizontal origin (columns).
func (c *TableInspectContext) ScrollX() int { return c.scrollX }

// ScrollY returns the current vertical origin (lines).
func (c *TableInspectContext) ScrollY() int { return c.scrollY }

// State returns the installed TabbedPopup or nil.
func (c *TableInspectContext) State() *popup.TabbedPopup { return c.state }

// SetLoading toggles the loading indicator. When true, HandleRender
// emits a "Loading…" body instead of delegating to state.Body().
func (c *TableInspectContext) SetLoading(v bool) { c.loading = v }

// IsLoading reports the current loading flag.
func (c *TableInspectContext) IsLoading() bool { return c.loading }

// Target returns the schema/table the popup is currently inspecting.
// Zero values when unset.
func (c *TableInspectContext) Target() (schema, table string) {
	return c.schema, c.table
}

// SetTarget records the schema/table the popup is inspecting. Pure
// state; callers (T9) are responsible for triggering refetches.
func (c *TableInspectContext) SetTarget(schema, table string) {
	c.schema = schema
	c.table = table
}

// HandleRender writes the popup body into the gocui view. Panels are
// responsible for SafeText-ing DB-supplied leaves (AD-17); the context
// must NOT re-strip the composed body — doing so destroys legitimate
// ANSI escapes from the active-tab header and the newlines between
// header and rows.
func (c *TableInspectContext) HandleRender() error {
	body := "Loading…"
	if !c.loading {
		if c.state != nil {
			body = c.state.Body()
		} else {
			body = ""
		}
	}
	viewName := c.GetViewName()
	writeView(c.deps, func() error {
		return c.deps.GuiDriver.SetContent(viewName, body)
	})
	return nil
}
