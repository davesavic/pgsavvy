package context

// SuggestionsContext renders the suggestions popup attached to a prompt.
// Suggestion fetching and selection wiring land in later epics.
type SuggestionsContext struct {
	BaseContext

	deps Deps
}

// NewSuggestionsContext builds a SuggestionsContext bound to SUGGESTIONS.
func NewSuggestionsContext(base BaseContext, deps Deps) *SuggestionsContext {
	return &SuggestionsContext{BaseContext: base, deps: deps}
}
