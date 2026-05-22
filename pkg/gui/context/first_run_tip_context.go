package context

// FirstRunTipContext renders the welcome popup shown above CONNECTIONS
// on the user's first launch. Kind = PERSISTENT_POPUP so subsequent popup
// pushes do not auto-evict it (AD-1 / dbsavvy-56u.2).
//
// The popup's copy comes from deps.FirstRunTipText (typically
// Tr.FirstRunTipTitle / Tr.FirstRunTipBody). The dismiss handler is
// installed by the orchestrator via a context-scoped action binding
// (TipDismiss); this context owns no input bindings itself — the
// orchestrator drives push/pop + StampStartupTips.
type FirstRunTipContext struct {
	BaseContext

	deps Deps
}

// NewFirstRunTipContext builds the context bound to FIRST_RUN_TIP.
func NewFirstRunTipContext(base BaseContext, deps Deps) *FirstRunTipContext {
	return &FirstRunTipContext{BaseContext: base, deps: deps}
}

// HandleRender writes the title + body to the FIRST_RUN_TIP view. The
// content comes from deps.FirstRunTipText; both a nil hook and a nil
// GuiDriver are silent no-ops so tests + partial wiring don't panic.
func (f *FirstRunTipContext) HandleRender() error {
	if f.deps.FirstRunTipText == nil {
		return nil
	}
	title, body := f.deps.FirstRunTipText()
	// Two-line render: title + blank-line + body. Keeping the format
	// intentionally minimal — the layout pass owns the border and the
	// popup's visual frame.
	text := title + "\n\n" + body
	viewName := f.GetViewName()
	writeView(f.deps, func() error {
		return f.deps.GuiDriver.SetContent(viewName, text)
	})
	return nil
}
