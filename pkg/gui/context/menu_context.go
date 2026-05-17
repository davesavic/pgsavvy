package context

// MenuContext renders the menu popup. The popup body and bindings are
// populated by the popup helper in later epics; T2 ships the lifecycle
// skeleton only.
type MenuContext struct {
	BaseContext

	deps Deps
}

// NewMenuContext builds a MenuContext bound to MENU.
func NewMenuContext(base BaseContext, deps Deps) *MenuContext {
	return &MenuContext{BaseContext: base, deps: deps}
}
