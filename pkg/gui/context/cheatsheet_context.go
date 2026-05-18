package context

import (
	"sync"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// CheatsheetContext renders the auto-generated keybinding cheatsheet
// (DISPLAY_CONTEXT). When the user presses `?` the orchestrator
// captures the focused scope, calls SetScope(scope) on this context,
// then pushes it onto the focus stack. The next layout pass calls
// HandleRender which invokes the wired render closure and writes the
// resulting body into the CHEATSHEET view via the driver.
//
// Mirrors the WhichKeyContext pattern (dlp.6): pkg/gui/context cannot
// import pkg/gui/keys or pkg/cheatsheet directly (those packages would
// pull pkg/gui/keys into the import DAG of pkg/gui/context, which the
// architecture forbids). The orchestrator wires a closure that closes
// over the live TrieSet + Registry + Tr.
//
// AddKeybindingsFn is a no-op — DISPLAY_CONTEXT is read-only chrome.
// The `<esc>` pop binding is installed on the CHEATSHEET view via
// driver.SetKeybinding in orchestrator wireWithDriver.
type CheatsheetContext struct {
	BaseContext

	deps   depsAlias
	render func(scope types.ContextKey) string

	mu    sync.Mutex
	scope types.ContextKey
}

// NewCheatsheetContext builds the CHEATSHEET context. render may be nil
// at construction (orchestrator wires it post-NewContextTree); a nil
// render renders nothing on HandleRender.
func NewCheatsheetContext(
	base BaseContext,
	deps depsAlias,
	render func(scope types.ContextKey) string,
) *CheatsheetContext {
	return &CheatsheetContext{
		BaseContext: base,
		deps:        deps,
		render:      render,
	}
}

// SetScope records the focused-scope captured at the moment the user
// pressed `?`. The orchestrator's `help.cheatsheet` handler calls this
// BEFORE pushing the context onto the focus stack so HandleRender sees
// the correct scope.
//
// Concurrent-safe; HandleRender takes the same mutex when reading.
func (c *CheatsheetContext) SetScope(scope types.ContextKey) {
	c.mu.Lock()
	c.scope = scope
	c.mu.Unlock()
}

// Scope returns the most recently captured scope (the empty key when
// SetScope has never been called).
func (c *CheatsheetContext) Scope() types.ContextKey {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.scope
}

// SetRender installs the body-producing closure. The orchestrator wires
// this post-NewContextTree once the matcher + registry are live.
func (c *CheatsheetContext) SetRender(render func(scope types.ContextKey) string) {
	c.render = render
}

// HandleRender invokes the wired render closure and writes its output
// to the CHEATSHEET view through the driver. No-ops cleanly when the
// render closure is nil (e.g. early bootstrap) or when render returns
// the empty string.
func (c *CheatsheetContext) HandleRender() error {
	if c.render == nil {
		return nil
	}
	scope := c.Scope()
	body := c.render(scope)
	if body == "" {
		return nil
	}
	viewName := c.GetViewName()
	writeView(c.deps, func() error {
		return c.deps.GuiDriver.SetContent(viewName, body)
	})
	return nil
}

// AddKeybindingsFn drops the contributor — DISPLAY_CONTEXT views are
// read-only chrome.
func (c *CheatsheetContext) AddKeybindingsFn(_ types.KeybindingsFn) {}
