package context

// GlobalContext is the no-view GLOBAL_CONTEXT host for global
// keybindings (leader prefix, ":" command line, quit, etc.). It has no
// view name — GetViewName returns "" so the layout manager skips
// SetView for it.
type GlobalContext struct {
	BaseContext

	deps Deps
}

// NewGlobalContext builds the GlobalContext bound to GLOBAL with an
// empty view name.
func NewGlobalContext(base BaseContext, deps Deps) *GlobalContext {
	return &GlobalContext{BaseContext: base, deps: deps}
}
