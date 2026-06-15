package context

import (
	"sync"

	"github.com/davesavic/pgsavvy/pkg/gui/popup"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// CheatsheetContext renders the auto-generated keybinding cheatsheet
// (DISPLAY_CONTEXT). When the user presses `?` the orchestrator
// captures the focused scope, calls SetScope(scope) on this context,
// then pushes it onto the focus stack. The next layout pass calls
// HandleRender which invokes the wired render closure and writes the
// resulting body into the CHEATSHEET view via the driver.
//
// Mirrors the WhichKeyContext pattern: pkg/gui/context cannot
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

	mu      sync.Mutex
	scope   types.ContextKey
	state   *popup.TabbedPopup
	scrollY int
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

// SetState installs a TabbedPopup state object. When
// non-nil, HandleRender writes state.Body() into the view — replacing
// the single-scope render-closure path. The orchestrator's
// help.cheatsheet action builds a state with one tab per relevant scope
// before pushing the context. nil clears the state, restoring the
// legacy render-closure fallback.
//
// Concurrent-safe; HandleRender takes the same mutex when reading.
func (c *CheatsheetContext) SetState(s *popup.TabbedPopup) {
	c.mu.Lock()
	c.state = s
	c.scrollY = 0
	c.mu.Unlock()
}

// Scroll moves the vertical view offset by delta lines, clamping at the
// top (never negative). The upper bound is enforced by the layout pass,
// which knows the rendered content height and viewport rows. delta < 0
// scrolls up; delta > 0 scrolls down.
func (c *CheatsheetContext) Scroll(delta int) {
	c.mu.Lock()
	c.scrollY += delta
	if c.scrollY < 0 {
		c.scrollY = 0
	}
	c.mu.Unlock()
}

// SetScrollY sets the absolute vertical offset, clamping at the top. The
// layout pass calls this to write back the offset it clamped to the
// content's max scroll (so `G` / over-scroll settle at the last page).
func (c *CheatsheetContext) SetScrollY(y int) {
	c.mu.Lock()
	if y < 0 {
		y = 0
	}
	c.scrollY = y
	c.mu.Unlock()
}

// ScrollY returns the current vertical view offset (lines).
func (c *CheatsheetContext) ScrollY() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.scrollY
}

// State returns the installed TabbedPopup or nil. Concurrent-safe.
func (c *CheatsheetContext) State() *popup.TabbedPopup {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// HandleRender writes the cheatsheet body into the gocui view. When a
// TabbedPopup state has been installed (the Z1 path) the active tab's
// body wins; otherwise HandleRender falls back to the single-scope
// render closure so legacy callers (test fixtures, pre-Z1 wiring) keep
// working. No-ops cleanly when both inputs are unset.
func (c *CheatsheetContext) HandleRender() error {
	state := c.State()
	var body string
	if state != nil {
		body = state.Body()
	}
	if body == "" {
		if c.render == nil {
			return nil
		}
		body = c.render(c.Scope())
	}
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
