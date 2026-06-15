package types

// Views aggregates every named *gocui.View handle the pgsavvy TUI binds
// a Context to. Populated by the layout manager once SetView returns a
// non-nil handle for each name; consumers should treat nil fields as
// "not yet laid out" rather than as a fatal error.
type Views struct {
	Connections View
	Schemas     View
	Tables      View
	Columns     View
	Indexes     View

	Main      View
	Secondary View

	Menu         View
	Confirmation View
	Prompt       View
	Suggestions  View
	History      View
	WhichKey     View

	Extras View
	Limit  View

	AppStatus View
	Options   View
	Search    View
}
