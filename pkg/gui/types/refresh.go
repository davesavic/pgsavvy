package types

// RefreshScope identifies which subsystem a RefreshOptions request
// targets. Concrete scope values are introduced by downstream epics; the
// type exists now so handler signatures stay stable.
type RefreshScope int

// RefreshMode classifies a refresh as synchronous, async, or
// content-only (the UpdateContentOnly fast path). Concrete values land
// with the refresh helper in a later epic.
type RefreshMode int

// RefreshOptions describes a requested re-render. Empty zero value is
// "refresh everything synchronously".
type RefreshOptions struct {
	Scope RefreshScope
	Mode  RefreshMode
}

// OnFocusOpts is delivered to IBaseContext.HandleFocus when focus moves
// to a Context. NewContextKey is the key being focused; the Clicked*
// fields are populated only when the focus change originated from a
// mouse click.
type OnFocusOpts struct {
	NewContextKey     ContextKey
	ClickedWindowName string
	ClickedViewName   string
}

// OnFocusLostOpts is delivered to IBaseContext.HandleFocusLost when
// focus moves away from a Context. NewContextKey identifies the Context
// gaining focus, so the losing Context can decide whether to persist
// state.
type OnFocusLostOpts struct {
	NewContextKey ContextKey
}
