package context

// DisplayLeafContext is a minimal, stateless leaf for a runtime-built
// TabbedRailContext (cheatsheet, FK picker): it renders a fixed body string into
// the container's shared view and handles nothing else. Runtime leaves are NOT
// registered in setup.go, so they cannot rely on setup's string(spec.key) view
// fallback — the container view name is constructor-injected and returned by
// GetViewName so the leaf's SetContent targets the SAME view the container's
// SetViewTabs does (many-contexts-ONE-view topology).
//
// It DELIBERATELY implements NEITHER BodyText (bodyTextRenderer) NOR FlushDirty
// (dirtyFlusher): the container therefore takes the leaf-delegation render path
// (calling HandleRender below) and never enrols the leaf in flushInactiveDirty.
//
// Concurrency: N/A — runs on the single gocui MainLoop (UI thread).
type DisplayLeafContext struct {
	BaseContext

	deps     Deps
	viewName string
	body     string
}

// NewDisplayLeafContext builds a display leaf bound to the container view name
// (which MUST equal the container's GetViewName) with the body to render.
func NewDisplayLeafContext(base BaseContext, deps Deps, viewName, body string) *DisplayLeafContext {
	return &DisplayLeafContext{
		BaseContext: base,
		deps:        deps,
		viewName:    viewName,
		body:        body,
	}
}

// GetViewName returns the constructor-injected container view name, overriding
// the embedded BaseContext so the leaf writes into the shared container view.
func (c *DisplayLeafContext) GetViewName() string { return c.viewName }

// HandleRender writes the body into the shared container view via the standard
// writeView idiom (nil-driver-safe, swallows ErrUnknownView on a transient
// view).
func (c *DisplayLeafContext) HandleRender() error {
	writeView(c.deps, func() error {
		return c.deps.GuiDriver.SetContent(c.viewName, c.body)
	})
	return nil
}
