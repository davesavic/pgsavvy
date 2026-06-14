package context

// ExportMenuState is the renderer-facing surface ExportMenuContext reads
// each frame. *ui.ResultTabsHelper satisfies this through its
// ExportMenuState accessor returning the menu's Body() string. Active()
// reports whether the menu is currently waiting.
type ExportMenuState interface {
	Active() bool
	Body() string
}

// ExportMenuContext renders the <leader>oe export menu. Mirrors
// HideOverlayContext: the helper owns the menu state object, the context
// only renders its Body() into the gocui view.
type ExportMenuContext struct {
	BaseContext

	deps  Deps
	state ExportMenuState
}

// NewExportMenuContext builds an ExportMenuContext bound to EXPORT_MENU.
func NewExportMenuContext(base BaseContext, deps Deps) *ExportMenuContext {
	return &ExportMenuContext{BaseContext: base, deps: deps}
}

// SetState installs the state reader. Nil-safe: HandleRender no-ops when
// no state is set or when state.Active() returns false.
func (e *ExportMenuContext) SetState(st ExportMenuState) { e.state = st }

// HandleRender writes the menu body into the gocui view. The body is
// fully assembled by popup.ExportMenu.Body() — no per-frame styling here.
func (e *ExportMenuContext) HandleRender() error {
	if e.state == nil || !e.state.Active() {
		return nil
	}
	body := e.state.Body()
	viewName := e.GetViewName()
	writeView(e.deps, func() error {
		return e.deps.GuiDriver.SetContent(viewName, body)
	})
	return nil
}
