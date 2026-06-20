package context

import (
	"sync"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// CheatsheetContext is the DISPLAY_CONTEXT container that multiplexes the
// per-category cheatsheet tabs into the single CHEATSHEET view
// (many-contexts-ONE-view topology). It is a THIN ADAPTER over the shared
// TabbedRailContext core: all tabbed-pane mechanics (tab switching, tab-strip
// publishing, leaf-delegation render) live in the embedded core; this type
// adds the captured focus scope and the per-tab vertical scroll the layout
// pass clamps via applyCheatsheetScroll (layout.go).
//
// When the user presses `?` the orchestrator captures the focused scope, calls
// SetScope(scope), builds one DisplayLeafContext per cheatsheet Category, calls
// SetTabs, then pushes the container onto the focus stack. The categorize/render
// happens in the orchestrator (pkg/gui/context must NOT import pkg/cheatsheet);
// the container only receives pre-rendered body strings via the leaves.
//
// The container constructs the core with FireFocusHooks=FALSE (every leaf lives
// under the single CHEATSHEET scope, so a tab switch is not a focus transition)
// and forces ManagesOwnOrigin=TRUE on every tab (see SetTabs): applyCheatsheetScroll
// is the SOLE writer of the CHEATSHEET view origin, so the core's generic per-tab
// origin save/restore is disabled.
//
// `<esc>` is a CHEATSHEET-scope TRIE binding (CheatsheetController) PLUS the
// automatic installEscAbortShim on non-editable views — NOT a driver
// SetKeybinding. No view-level esc binding is installed here.
type CheatsheetContext struct {
	*TabbedRailContext

	deps Deps

	mu    sync.Mutex
	scope types.ContextKey

	// scroll holds the per-tab vertical view offset (lines), sized to the tab
	// count by SetTabs and indexed by the embedded core's ActiveTab(). This
	// context owns only the top clamp (>= 0); the layout pass
	// (applyCheatsheetScroll, layout.go) owns the bottom clamp against the
	// rendered content extent and is the single writer of the gocui view origin.
	scroll []int
}

// NewCheatsheetContext builds the CHEATSHEET container as a thin adapter over a
// TabbedRailContext core with NO initial tabs (the per-category tabs are built
// and injected at runtime via SetTabs each time `?` is pressed).
func NewCheatsheetContext(base BaseContext, deps Deps) *CheatsheetContext {
	core := NewTabbedRailContext(base, deps, TabbedRailOpts{
		// FireFocusHooks=false: every leaf shares the single CHEATSHEET scope,
		// so a tab switch is NOT a focus transition — firing per-leaf focus
		// hooks would be spurious. The leaves are stateless body renderers.
		FireFocusHooks: false,
	})
	return &CheatsheetContext{
		TabbedRailContext: core,
		deps:              deps,
	}
}

// SetScope records the focused-scope captured at the moment the user pressed
// `?`. The orchestrator's HelpCheatsheet handler calls this BEFORE pushing the
// context onto the focus stack. Concurrent-safe.
func (c *CheatsheetContext) SetScope(scope types.ContextKey) {
	c.mu.Lock()
	c.scope = scope
	c.mu.Unlock()
}

// Scope returns the most recently captured scope (the empty key when SetScope
// has never been called).
func (c *CheatsheetContext) Scope() types.ContextKey {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.scope
}

// SetTabs rebuilds the container's tab set from a fresh spec list, forcing
// ManagesOwnOrigin=true on every spec (applyCheatsheetScroll, layout.go, is the
// SOLE writer of the CHEATSHEET view origin, so the core's restoreActiveOrigin
// is a no-op), re-allocates the per-tab scroll store (zeroing every tab's offset
// so a fresh `?` opens at the top), then delegates to the embedded core.
func (c *CheatsheetContext) SetTabs(specs []TabSpec, leaves []types.IBaseContext) {
	for i := range specs {
		specs[i].ManagesOwnOrigin = true
	}
	c.scroll = make([]int, len(specs))
	c.TabbedRailContext.SetTabs(specs, leaves)
}

// activeScroll returns a pointer to the active tab's stored vertical offset, or
// nil when the scroll store is empty / the active index is out of range. Guards
// the scroll accessors against an unsized store.
func (c *CheatsheetContext) activeScroll() *int {
	idx := c.ActiveTab()
	if idx < 0 || idx >= len(c.scroll) {
		return nil
	}
	return &c.scroll[idx]
}

// ScrollY returns the ACTIVE tab's vertical offset (lines). Zero when the scroll
// store is unsized.
func (c *CheatsheetContext) ScrollY() int {
	if s := c.activeScroll(); s != nil {
		return *s
	}
	return 0
}

// SetScrollY sets the ACTIVE tab's absolute vertical offset, clamping at the top
// (never negative). The layout pass calls this to write back the value it
// clamped to the content's last page (so `G` / over-scroll settle on the last
// page). No-op when the scroll store is unsized.
func (c *CheatsheetContext) SetScrollY(y int) {
	s := c.activeScroll()
	if s == nil {
		return
	}
	if y < 0 {
		y = 0
	}
	*s = y
}

// Scroll moves the ACTIVE tab's vertical offset by delta lines, clamping at the
// top. The bottom bound is enforced by the layout pass, which knows the rendered
// content height. delta < 0 scrolls up; delta > 0 scrolls down.
func (c *CheatsheetContext) Scroll(delta int) {
	c.SetScrollY(c.ScrollY() + delta)
}
