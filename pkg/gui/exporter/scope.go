package exporter

// Scope identifies which row subset is exported.
type Scope int

const (
	ScopeVisible Scope = iota // current viewport range
	ScopeLoaded               // all rows in buffer, with active filter+sort projection applied
	ScopeFull                 // ALL server rows; ignores active filter; arrival order
)

func (s Scope) String() string {
	switch s {
	case ScopeVisible:
		return "Visible"
	case ScopeLoaded:
		return "Loaded"
	case ScopeFull:
		return "Full"
	}
	return "Unknown"
}
