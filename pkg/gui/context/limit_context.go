package context

// LimitContext renders the terminal-too-small overlay (DISPLAY_CONTEXT
// kind). The displayed text comes from deps.LimitText so i18n / theming
// (T8/T9) supply the actual copy without touching this file.
type LimitContext struct {
	BaseContext

	deps Deps
}

// NewLimitContext builds the LimitContext bound to LIMIT.
func NewLimitContext(base BaseContext, deps Deps) *LimitContext {
	return &LimitContext{BaseContext: base, deps: deps}
}

// HandleRender writes the limit overlay text to the LIMIT view. The text
// comes from deps.LimitText (typically Tr.TerminalTooSmall); both a nil
// hook and a nil GuiDriver are silent no-ops.
func (l *LimitContext) HandleRender() error {
	if l.deps.LimitText == nil {
		return nil
	}
	text := l.deps.LimitText()
	viewName := l.GetViewName()
	writeView(l.deps, func() error {
		return l.deps.GuiDriver.SetContent(viewName, text)
	})
	return nil
}
