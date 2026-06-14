package types

// TextStyle is the minimal shape ContextTreeDeps.PresentationHook returns
// so context code can stay decoupled from the full style builder shipped
// by epic T8. T8 may refine this struct (replacing the string
// colour fields with concrete style.Color values); contexts only consume
// the struct as a value and do not depend on its internal representation.
type TextStyle struct {
	// Fg is the foreground colour token (e.g. "#ff8800" or a theme name).
	// Empty means "use the default".
	Fg string
	// Bg is the background colour token. Empty means "use the default".
	Bg string
	// Bold, when true, renders the text in bold.
	Bold bool
}
