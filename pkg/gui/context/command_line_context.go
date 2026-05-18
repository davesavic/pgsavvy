package context

import (
	"strings"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// CommandLineContext renders the colon ex-command line at the bottom of
// the screen. Kind=TEMPORARY_POPUP — pushed on `:` (via the command.open
// action) and popped on <esc> / <cr> (via command.cancel / command.submit).
//
// Runtime source of truth for the typed line is the underlying gocui
// View's TextArea: the master gocui.Editor (installed in dlp.8b) routes
// COMMAND_LINE keystrokes through the Matcher, and Passthrough delegates
// to gocui.DefaultEditor which writes directly into v.TextArea then
// calls v.RenderTextArea. The orchestrator plumbs the *gocui.View handle
// in via SetView each Layout frame; ReadAndClearBuffer pulls the typed
// text from v.TextArea.GetContent (stripping the leading ":") and
// clears it. The buf field below remains as a test-only seam: SetBuffer
// is used by unit tests that don't wire a real view.
type CommandLineContext struct {
	BaseContext
	deps  depsAlias
	modes types.ModeSetter
	buf   string
	view  types.View
}

// NewCommandLineContext constructs the context. modes may be nil
// (test wiring); the focus hooks become no-ops in that case.
func NewCommandLineContext(base BaseContext, deps depsAlias, modes types.ModeSetter) *CommandLineContext {
	return &CommandLineContext{BaseContext: base, deps: deps, modes: modes}
}

// HandleFocus marks the per-scope mode as ModeCommand so the Matcher's
// Insert/Command passthrough routes printable runes into the master
// Editor (dlp.8b) instead of count-collection / register-prefix logic.
func (c *CommandLineContext) HandleFocus(_ types.OnFocusOpts) error {
	if c.modes != nil {
		c.modes.Set(types.COMMAND_LINE, types.ModeCommand)
	}
	return nil
}

// HandleFocusLost resets the ModeStore entry and clears any half-typed
// buffer. A subsequent push starts with a clean slate. The view pointer
// is dropped here too: the orchestrator DeleteView's the COMMAND_LINE
// view on pop and recreates it on re-Push, so any cached pointer would
// dangle until the next Layout frame re-plumbs it.
func (c *CommandLineContext) HandleFocusLost(_ types.OnFocusLostOpts) error {
	if c.modes != nil {
		c.modes.Reset(types.COMMAND_LINE)
	}
	c.buf = ""
	c.view = nil
	return nil
}

// HandleRender is a no-op for COMMAND_LINE: the master gocui.Editor's
// Passthrough path writes user-typed runes into v.TextArea and calls
// v.RenderTextArea on every keystroke; the leading ":" prompt is
// prepopulated by the orchestrator's Layout Tier-3 pass when the view
// is first created (gocui.ErrUnknownView sentinel). Calling SetContent
// here would overwrite v.lines after RenderTextArea, erasing the
// typed text every frame.
func (c *CommandLineContext) HandleRender() error { return nil }

// SetView is called by the orchestrator's Layout Tier-3 popup pass each
// frame the COMMAND_LINE is on the focus stack. ReadAndClearBuffer
// reads typed text from the supplied view's TextArea.
func (c *CommandLineContext) SetView(v types.View) { c.view = v }

// SetBuffer replaces the test-mode typed buffer. Real runtime uses
// v.TextArea via SetView. Retained so existing unit tests (which don't
// wire a view) keep compiling.
func (c *CommandLineContext) SetBuffer(s string) { c.buf = s }

// Buffer returns the current typed buffer. Reads from v.TextArea when
// a view has been plumbed in; otherwise returns the test-mode buf.
// The leading ":" prompt is stripped so callers see only the typed
// command text.
func (c *CommandLineContext) Buffer() string {
	if c.view != nil && c.view.TextArea != nil {
		return strings.TrimPrefix(c.view.TextArea.GetContent(), ":")
	}
	return c.buf
}

// ReadAndClearBuffer returns the typed text and resets it to "". Used
// by command.submit to atomically consume the line before popping the
// context. When a view is plumbed in, the TextArea is the source of
// truth; otherwise the test-mode buf is used.
func (c *CommandLineContext) ReadAndClearBuffer() string {
	if c.view != nil && c.view.TextArea != nil {
		s := strings.TrimPrefix(c.view.TextArea.GetContent(), ":")
		c.view.TextArea.Clear()
		return s
	}
	s := c.buf
	c.buf = ""
	return s
}
