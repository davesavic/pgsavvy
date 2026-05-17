package context

// CommandLogContext renders the command-log panel in the EXTRAS slot.
// Log line buffering and append wiring land with the command log helper
// in a later epic.
type CommandLogContext struct {
	BaseContext

	deps Deps
}

// NewCommandLogContext builds a CommandLogContext bound to LOG.
func NewCommandLogContext(base BaseContext, deps Deps) *CommandLogContext {
	return &CommandLogContext{BaseContext: base, deps: deps}
}
