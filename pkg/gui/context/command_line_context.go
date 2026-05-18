package context

import (
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// CommandLineContext renders the colon ex-command line at the bottom of
// the screen. Kind=TEMPORARY_POPUP — pushed on `:` (via the command.open
// action) and popped on <esc> / <cr> (via command.cancel / command.submit).
//
// The typed buffer is held on the context (set externally by the master
// Editor's Passthrough path in dlp.8b; tests drive it via SetBuffer).
// HandleFocus sets ModeStore[COMMAND_LINE] = ModeCommand so the Matcher
// switches its passthrough fast-path for printable runes; HandleFocusLost
// resets the entry and clears the buffer so a re-push starts empty.
type CommandLineContext struct {
	BaseContext
	deps  depsAlias
	modes types.ModeSetter
	buf   string
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
// buffer. A subsequent push starts with a clean slate.
func (c *CommandLineContext) HandleFocusLost(_ types.OnFocusLostOpts) error {
	if c.modes != nil {
		c.modes.Reset(types.COMMAND_LINE)
	}
	c.buf = ""
	return nil
}

// HandleRender writes the ":<buf>" prompt into the view. No-op when the
// driver is nil (unit tests with no driver wired).
func (c *CommandLineContext) HandleRender() error {
	viewName := c.GetViewName()
	body := ":" + c.buf
	writeView(c.deps, func() error {
		return c.deps.GuiDriver.SetContent(viewName, body)
	})
	return nil
}

// SetBuffer replaces the typed buffer. Called by the master Editor's
// Passthrough path (dlp.8b) and by tests.
func (c *CommandLineContext) SetBuffer(s string) { c.buf = s }

// Buffer returns the current typed buffer.
func (c *CommandLineContext) Buffer() string { return c.buf }

// ReadAndClearBuffer returns the buffer's contents and resets it to "".
// Used by command.submit to atomically consume the typed line before
// popping the context.
func (c *CommandLineContext) ReadAndClearBuffer() string {
	s := c.buf
	c.buf = ""
	return s
}
