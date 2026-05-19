package context

import (
	"strings"
)

// SelectionState is the renderer-facing surface SelectionContext reads
// each frame. *ui.ChoiceHelper satisfies this directly — Active / Label
// / Choices / Cursor all live there. The orchestrator wires the helper
// via SelectionContext.SetState after constructing it (the helper holds
// a pointer back to *SelectionContext, so the registry must be built
// first).
type SelectionState interface {
	Active() bool
	Label() string
	Choices() []string
	Cursor() int
}

// SelectionContext renders the list-style selection popup (driver picker
// and similar pickers in the connection-add flow).
type SelectionContext struct {
	BaseContext

	deps  Deps
	state SelectionState
}

// NewSelectionContext builds a SelectionContext bound to SELECTION.
func NewSelectionContext(base BaseContext, deps Deps) *SelectionContext {
	return &SelectionContext{BaseContext: base, deps: deps}
}

// SetState installs the state reader. Nil-safe: HandleRender no-ops when
// no state is set.
func (s *SelectionContext) SetState(st SelectionState) { s.state = st }

// HandleRender writes the popup body — label on the first line, then
// one line per choice with a "> " marker on the cursor row and a "  "
// indent on the rest so the alignment is stable.
func (s *SelectionContext) HandleRender() error {
	if s.state == nil || !s.state.Active() {
		return nil
	}
	body := formatSelectionBody(s.state.Label(), s.state.Choices(), s.state.Cursor())
	viewName := s.GetViewName()
	writeView(s.deps, func() error {
		return s.deps.GuiDriver.SetContent(viewName, body)
	})
	return nil
}

func formatSelectionBody(label string, choices []string, cursor int) string {
	var b strings.Builder
	b.WriteString(label)
	b.WriteByte('\n')
	for i, choice := range choices {
		b.WriteByte('\n')
		if i == cursor {
			b.WriteString("> ")
		} else {
			b.WriteString("  ")
		}
		b.WriteString(choice)
	}
	return b.String()
}
