package context

import (
	"strings"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// searchPrefix is the leading glyph rendered before the typed query on
// the SearchLine, mirroring vim's "/" forward-search prompt.
const searchPrefix = "/"

// SearchLineContext renders the bottom-anchored single-line in-grid
// search input. Kind=TEMPORARY_POPUP — pushed when
// the user opens search and popped on <cr> / <esc>.
//
// It is the SearchLine analogue of CommandLineContext: same
// PopupSizeCommandLine geometry, same TextArea + master-editor
// Passthrough input mechanism. The two differences are the rendered
// prefix ("/" instead of ":") and a right-aligned match-count slot.
//
// Runtime source of truth for the typed query is the underlying gocui
// View's TextArea. Unlike COMMAND_LINE the TextArea holds ONLY the raw
// query (no leading prefix); HandleRender re-draws the "/" prefix and
// the match-count slot via SetContent each frame so neither lives in
// the editable buffer. The buf field below is a test-only seam:
// SetBuffer is used by unit tests that don't wire a real view.
type SearchLineContext struct {
	BaseContext
	deps       depsAlias
	modes      types.ModeSetter
	buf        string
	view       types.View
	matchCount string
	width      int
}

// NewSearchLineContext constructs the context. modes may be nil (test
// wiring); the focus hooks become no-ops in that case.
func NewSearchLineContext(base BaseContext, deps depsAlias, modes types.ModeSetter) *SearchLineContext {
	return &SearchLineContext{BaseContext: base, deps: deps, modes: modes}
}

// HandleFocus marks the SEARCH_LINE scope as ModeCommand so the
// Matcher's passthrough routes printable runes into the master Editor
// (mirrors CommandLineContext.HandleFocus).
func (c *SearchLineContext) HandleFocus(_ types.OnFocusOpts) error {
	if c.modes != nil {
		c.modes.Set(types.SEARCH_LINE, types.ModeCommand)
	}
	return nil
}

// HandleFocusLost resets the ModeStore entry and clears any half-typed
// buffer + cached view pointer. A subsequent push starts clean.
func (c *SearchLineContext) HandleFocusLost(_ types.OnFocusLostOpts) error {
	if c.modes != nil {
		c.modes.Reset(types.SEARCH_LINE)
	}
	c.buf = ""
	c.view = nil
	c.matchCount = ""
	return nil
}

// HandleRender writes the single bottom line: the "/" prefix followed by
// the typed query on the left, and the match-count slot right-aligned to
// the view width. When the width is unknown (recorder-driver / pre-layout
// path) the count is appended after a single space.
func (c *SearchLineContext) HandleRender() error {
	body := c.renderLine()
	viewName := c.GetViewName()
	writeView(c.deps, func() error {
		return c.deps.GuiDriver.SetContent(viewName, body)
	})
	return nil
}

// renderLine builds the SearchLine cell content: left = "/" + query,
// right = match-count slot. Right-aligned to width when known.
func (c *SearchLineContext) renderLine() string {
	left := searchPrefix + c.Buffer()
	if c.matchCount == "" {
		return left
	}
	pad := c.width - len(left) - len(c.matchCount)
	if pad < 1 {
		return left + " " + c.matchCount
	}
	return left + strings.Repeat(" ", pad) + c.matchCount
}

// SetView is called by the orchestrator's Layout popup pass each frame
// the SEARCH_LINE is on the focus stack.
func (c *SearchLineContext) SetView(v types.View) { c.view = v }

// SetWidth records the view's inner width used to right-align the
// match-count slot. The layout pass calls this each frame (wired by the
// grid-search task); a zero width disables right-alignment.
func (c *SearchLineContext) SetWidth(w int) { c.width = w }

// SetMatchCount sets the right-aligned count slot text (e.g. "3/12").
// Empty hides the slot.
func (c *SearchLineContext) SetMatchCount(s string) { c.matchCount = s }

// SetBuffer replaces the test-mode typed buffer. Real runtime uses
// v.TextArea via SetView.
func (c *SearchLineContext) SetBuffer(s string) { c.buf = s }

// Buffer returns the current typed query. Reads from v.TextArea when a
// view has been plumbed in; otherwise returns the test-mode buf. No
// prefix stripping is needed — the TextArea holds only the raw query.
func (c *SearchLineContext) Buffer() string {
	if c.view != nil && c.view.TextArea != nil {
		return c.view.TextArea.GetContent()
	}
	return c.buf
}

// ReadAndClearBuffer returns the typed query and resets it to "". When a
// view is plumbed in the TextArea is the source of truth; otherwise the
// test-mode buf is used.
func (c *SearchLineContext) ReadAndClearBuffer() string {
	if c.view != nil && c.view.TextArea != nil {
		s := c.view.TextArea.GetContent()
		c.view.TextArea.Clear()
		return s
	}
	s := c.buf
	c.buf = ""
	return s
}
