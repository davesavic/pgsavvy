package context

import (
	"strings"
)

// PromptState is the renderer-facing surface PromptContext.HandleRender
// reads each frame. *ui.PromptHelper holds the label + active flag;
// *controllers.PromptController holds the typed buffer. The orchestrator
// supplies a small adapter that combines them and registers it via
// PromptContext.SetState.
//
// All methods are called synchronously from the layout pass; the helpers
// already guard their internal state with a mutex.
type PromptState interface {
	Active() bool
	Label() string
	Buffer() string
}

// PromptContext renders the single-line prompt popup.
type PromptContext struct {
	BaseContext

	deps  Deps
	state PromptState
}

// NewPromptContext builds a PromptContext bound to PROMPT. The state
// reader is wired post-construction via SetState — at construction time
// the PromptHelper / PromptController do not exist yet (the orchestrator
// builds them after the context registry).
func NewPromptContext(base BaseContext, deps Deps) *PromptContext {
	return &PromptContext{BaseContext: base, deps: deps}
}

// SetState installs the state reader. Nil-safe: HandleRender no-ops when
// no state is set.
func (p *PromptContext) SetState(s PromptState) { p.state = s }

// HandleRender writes the popup body — label on the first line, the
// typed buffer with a "> " prefix on the second. No-op when no state is
// wired or when the helper reports inactive (the popup is on the focus
// stack but the helper hasn't been told what to prompt for yet).
func (p *PromptContext) HandleRender() error {
	if p.state == nil || !p.state.Active() {
		return nil
	}
	body := formatPromptBody(p.state.Label(), p.state.Buffer())
	viewName := p.GetViewName()
	writeView(p.deps, func() error {
		return p.deps.GuiDriver.SetContent(viewName, body)
	})
	return nil
}

// CursorXY returns the (x, y) coordinates the layout pass should pass
// to GuiDriver.SetViewCursor to anchor the visible caret at the end of
// the typed buffer. The body format from formatPromptBody is:
//
//	line 0: <label>
//	line 1: (blank)
//	line 2: "> " + <buffer>
//
// so y = 2 and x = 2 + len(buffer) (the "> " prefix is two cells). ok is
// false when no state is wired or the prompt is inactive — callers must
// skip SetViewCursor in that case so the caret doesn't land on a popup
// that isn't visible. Mirrors the COMMAND_LINE branch's cursor anchoring
// (layout.go) but uses a context-level accessor because PromptContext
// has no TextArea-style cursor of its own; the buffer always ends at
// end-of-buffer per dbsavvy-m47 (no left/right navigation).
func (p *PromptContext) CursorXY() (int, int, bool) {
	if p.state == nil || !p.state.Active() {
		return 0, 0, false
	}
	return 2 + len(p.state.Buffer()), 2, true
}

// formatPromptBody returns the popup body — label, blank separator,
// then the typed buffer with a "> " prefix so the entry line is visually
// distinct from the label.
func formatPromptBody(label, buffer string) string {
	var b strings.Builder
	b.WriteString(label)
	b.WriteString("\n\n> ")
	b.WriteString(buffer)
	return b.String()
}
