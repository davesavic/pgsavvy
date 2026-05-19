package context

// MessagesContext renders the messages panel in the EXTRAS slot, showing
// server-emitted PG NOTICE/WARNING/INFO lines and (per DESIGN.md §12.5.7
// / §14) future commit-edits audit and DDL output:log routing.
type MessagesContext struct {
	BaseContext

	deps Deps
}

// NewMessagesContext builds a MessagesContext bound to MESSAGES.
func NewMessagesContext(base BaseContext, deps Deps) *MessagesContext {
	return &MessagesContext{BaseContext: base, deps: deps}
}
