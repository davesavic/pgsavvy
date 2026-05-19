package context

// SelectionContext renders the list-style selection popup (driver picker
// and similar pickers in the connection-add flow). Like PromptContext
// this is deliberately stateless: the cursor / label / choices live on
// the ChoiceHelper, and the SelectionController reads them directly.
// Keeping the context shape parallel to PromptContext (BaseContext +
// Deps only) means dbsavvy-m47.2 doesn't fork two source-of-truth
// stories for cursor state.
type SelectionContext struct {
	BaseContext

	deps Deps
}

// NewSelectionContext builds a SelectionContext bound to SELECTION.
func NewSelectionContext(base BaseContext, deps Deps) *SelectionContext {
	return &SelectionContext{BaseContext: base, deps: deps}
}
