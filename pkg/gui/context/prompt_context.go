package context

// PromptContext renders the single-line prompt popup. The line editor and
// submit/cancel handlers are wired by the popup helper in later epics.
type PromptContext struct {
	BaseContext

	deps Deps
}

// NewPromptContext builds a PromptContext bound to PROMPT.
func NewPromptContext(base BaseContext, deps Deps) *PromptContext {
	return &PromptContext{BaseContext: base, deps: deps}
}
