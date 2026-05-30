package context

import (
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// ConnectingContext is the full-pane MAIN_CONTEXT screen shown while a
// connection attempt is in flight (epic dbsavvy-e53). When pushed it
// occupies the dims["main"] slot — the layout pass suppresses the
// QUERY_EDITOR paint for the frame this context is top of the focus
// stack and renders this view there instead.
//
// State is transient, in-memory, and NOT goroutine-safe: SetConnecting /
// SetError are plain setters. The T4 connect-IO caller is responsible for
// wrapping every state mutation in runOnUIThread so the read in
// HandleRender (which always runs on the MainLoop) never races a writer.
//
// Strings are hardcoded English (epic decision — i18n is intentionally
// not threaded through this context).
type ConnectingContext struct {
	BaseContext

	deps depsAlias

	// name is the profile name shown in the connecting state. Empty is
	// tolerated — HandleRender renders without crashing.
	name string
	// errMsg holds the failure message. When non-empty the context renders
	// the error state (message + retry/back hints) instead of the
	// connecting state.
	errMsg string
}

// Compile-time assertion that the live type satisfies the lifecycle
// contract.
var _ types.IBaseContext = (*ConnectingContext)(nil)

// NewConnectingContext builds the context bound to CONNECTING.
func NewConnectingContext(base BaseContext, deps depsAlias) *ConnectingContext {
	return &ConnectingContext{BaseContext: base, deps: deps}
}

// SetConnecting puts the context into the connecting state for the named
// profile, clearing any previous error. Plain setter — see the type doc:
// the T4 caller serialises this onto the UI thread.
func (c *ConnectingContext) SetConnecting(name string) {
	c.name = name
	c.errMsg = ""
}

// SetError puts the context into the error state with the supplied
// message. Plain setter — serialised onto the UI thread by the caller.
func (c *ConnectingContext) SetError(msg string) {
	c.errMsg = msg
}

// HandleRender writes the full-pane body to the CONNECTING view. A nil
// GuiDriver is a silent no-op (test wiring / partial bootstrap) so the
// call never panics. An empty profile name still renders a well-formed
// body.
func (c *ConnectingContext) HandleRender() error {
	body := c.body()
	viewName := c.GetViewName()
	writeView(c.deps, func() error {
		return c.deps.GuiDriver.SetContent(viewName, body)
	})
	return nil
}

// body returns the full-pane text for the current state: the error body
// when an error is set, otherwise the connecting body.
func (c *ConnectingContext) body() string {
	if c.errMsg != "" {
		return c.errMsg + "\n\n[r] retry  [Esc] back"
	}
	return "Connecting to " + c.name + "…"
}

// GetKind overrides BaseContext.GetKind to publish MAIN_CONTEXT,
// mirroring QueryEditorContext so a later refactor that drops the
// explicit kind in setup.go stays sound.
func (c *ConnectingContext) GetKind() types.ContextKind { return types.MAIN_CONTEXT }
