package context

// HideOverlayState is the renderer-facing surface HideOverlayContext
// reads each frame. *ui.ResultTabsHelper satisfies this through its
// HideOverlayState accessor returning *popup.HideOverlay (which exposes
// Body()). Active() reports whether the overlay is currently waiting.
type HideOverlayState interface {
	Active() bool
	Body() string
}

// HideOverlayContext renders the <leader>gH hide-cols overlay. Mirrors
// SelectionContext's shape: the helper owns the state object, the
// context only renders its Body() into the gocui view. Wiring lives in
// the orchestrator (the helper Push()es this context on overlay open
// and Pop()s on close).
type HideOverlayContext struct {
	BaseContext

	deps  Deps
	state HideOverlayState
}

// NewHideOverlayContext builds a HideOverlayContext bound to HIDE_OVERLAY.
func NewHideOverlayContext(base BaseContext, deps Deps) *HideOverlayContext {
	return &HideOverlayContext{BaseContext: base, deps: deps}
}

// SetState installs the state reader. Nil-safe: HandleRender no-ops when
// no state is set or when state.Active() returns false.
func (h *HideOverlayContext) SetState(st HideOverlayState) { h.state = st }

// HandleRender writes the overlay body (label + checklist) into the
// gocui view. The body is fully assembled by popup.HideOverlay.Body() —
// no per-frame styling here.
func (h *HideOverlayContext) HandleRender() error {
	if h.state == nil || !h.state.Active() {
		return nil
	}
	body := h.state.Body()
	viewName := h.GetViewName()
	writeView(h.deps, func() error {
		return h.deps.GuiDriver.SetContent(viewName, body)
	})
	return nil
}
