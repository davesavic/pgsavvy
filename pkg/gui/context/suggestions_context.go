package context

import (
	"strings"

	"github.com/davesavic/dbsavvy/pkg/gui/editor"
	"github.com/davesavic/dbsavvy/pkg/gui/grid"
)

// suggestionsVisibleMax bounds the number of suggestion rows rendered
// in the popup body. Excess suggestions remain in state and can be
// reached by scrolling Selected past the visible window (the renderer
// keeps Selected on-screen by sliding the window).
const suggestionsVisibleMax = 8

// SuggestionsContext renders the floating completion popup driven by
// the editor's completion Engine. Owns the popup state machine
// (visibility, selection cursor, anchor position) and emits the body
// text on HandleRender; the orchestrator owns view sizing / anchor
// placement (Z1).
type SuggestionsContext struct {
	BaseContext

	deps Deps

	visible     bool
	suggestions []editor.Suggestion
	selected    int
	anchor      editor.Position
}

// NewSuggestionsContext builds a SuggestionsContext bound to SUGGESTIONS.
func NewSuggestionsContext(base BaseContext, deps Deps) *SuggestionsContext {
	return &SuggestionsContext{BaseContext: base, deps: deps}
}

// Show installs suggestions + anchor and flips the popup visible.
// An empty suggestions slice leaves the popup hidden (there is
// nothing to render). Selected resets to 0.
func (c *SuggestionsContext) Show(suggestions []editor.Suggestion, anchor editor.Position) {
	if len(suggestions) == 0 {
		c.Hide()
		return
	}
	cp := make([]editor.Suggestion, len(suggestions))
	copy(cp, suggestions)
	c.suggestions = cp
	c.selected = 0
	c.anchor = anchor
	c.visible = true
}

// Hide clears the popup. The state (suggestions, selected) is dropped
// so the next Show starts fresh.
func (c *SuggestionsContext) Hide() {
	c.visible = false
	c.suggestions = nil
	c.selected = 0
	c.anchor = editor.Position{}
}

// IsVisible reports whether the popup should be rendered.
func (c *SuggestionsContext) IsVisible() bool { return c.visible }

// Next advances the selection cursor, wrapping at the bottom. No-op
// when the popup is hidden or has no suggestions.
func (c *SuggestionsContext) Next() {
	n := len(c.suggestions)
	if !c.visible || n == 0 {
		return
	}
	c.selected = (c.selected + 1) % n
}

// Prev reverses the selection cursor, wrapping at the top. No-op
// when the popup is hidden or has no suggestions.
func (c *SuggestionsContext) Prev() {
	n := len(c.suggestions)
	if !c.visible || n == 0 {
		return
	}
	c.selected--
	if c.selected < 0 {
		c.selected = n - 1
	}
}

// Selected returns the current cursor index. -1 when hidden / empty.
func (c *SuggestionsContext) Selected() int {
	if !c.visible || len(c.suggestions) == 0 {
		return -1
	}
	return c.selected
}

// Suggestions returns a copy of the current suggestion list for
// callers that need to inspect / audit the popup contents.
func (c *SuggestionsContext) Suggestions() []editor.Suggestion {
	if len(c.suggestions) == 0 {
		return nil
	}
	cp := make([]editor.Suggestion, len(c.suggestions))
	copy(cp, c.suggestions)
	return cp
}

// Anchor returns the editor position the popup is anchored to. Zero
// Position when hidden.
func (c *SuggestionsContext) Anchor() editor.Position { return c.anchor }

// Accept returns the currently-selected suggestion (with Text routed
// through editor.SanitizeText, guarding against control / newline
// bytes leaking into a Buffer insert) and hides the popup. Returns
// (_, false) when the popup is hidden, has no suggestions, or the
// cursor sits out of range.
func (c *SuggestionsContext) Accept() (editor.Suggestion, bool) {
	if !c.visible {
		return editor.Suggestion{}, false
	}
	if len(c.suggestions) == 0 {
		return editor.Suggestion{}, false
	}
	if c.selected < 0 || c.selected >= len(c.suggestions) {
		return editor.Suggestion{}, false
	}
	s := c.suggestions[c.selected]
	s.Text = editor.SanitizeText(s.Text)
	c.Hide()
	return s, true
}

// OnCursorMoved is the integration hook the vim editor controller
// calls on any motion / insert handler that should dismiss the popup
// (vim's omni-complete behaviour: any cursor move outside the
// active completion word closes the menu). Z1 wires this into the
// motion + insert mode handlers; for C1 the method exists as a
// no-op-safe sink the controller can call.
func (c *SuggestionsContext) OnCursorMoved() {
	if !c.visible {
		return
	}
	c.Hide()
}

// HandleRender writes the popup body via deps.GuiDriver.SetContent.
// No-op when hidden or driver-less. Suggestion.Display is routed
// through grid.SanitizeCellEscapes so untrusted server text cannot
// hijack the terminal.
func (c *SuggestionsContext) HandleRender() error {
	if !c.visible || len(c.suggestions) == 0 {
		return nil
	}
	body := formatSuggestionsBody(c.suggestions, c.selected, suggestionsVisibleMax)
	viewName := c.GetViewName()
	writeView(c.deps, func() error {
		return c.deps.GuiDriver.SetContent(viewName, body)
	})
	return nil
}

// formatSuggestionsBody renders one line per visible suggestion.
// Selected row gets the "> " marker; others get "  " so column
// alignment stays stable. The visible window slides to keep selected
// inside [start, start+visibleMax).
func formatSuggestionsBody(suggestions []editor.Suggestion, selected, visibleMax int) string {
	if visibleMax <= 0 {
		visibleMax = len(suggestions)
	}
	n := len(suggestions)
	start := 0
	if n > visibleMax {
		// Slide the window so selected stays on-screen.
		if selected >= visibleMax {
			start = selected - visibleMax + 1
		}
		if start+visibleMax > n {
			start = n - visibleMax
		}
		if start < 0 {
			start = 0
		}
	}
	end := start + visibleMax
	if end > n {
		end = n
	}
	var sb strings.Builder
	for i := start; i < end; i++ {
		if i > start {
			sb.WriteByte('\n')
		}
		if i == selected {
			sb.WriteString("> ")
		} else {
			sb.WriteString("  ")
		}
		sb.WriteString(grid.SanitizeCellEscapes(suggestions[i].Display))
	}
	return sb.String()
}
