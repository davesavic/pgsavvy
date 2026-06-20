package commands

// KnownTags is the canonical set of production Command.Tag values. Every
// non-empty Tag assigned to a registered Command MUST appear here. It is
// the authoritative list consumed by the cheatsheet Tag→Category taxonomy
// (pkg/cheatsheet/categories.go) and its tests: adding a new production
// tag without also categorizing it must break a test rather than silently
// bucket to General.
//
// The empty string Tag (NopCommand) is intentionally excluded — it is not
// a categorizable tag and maps to General by the taxonomy's fallback.
//
// The test-only tag "Table" (options_bar_disabled_test.go) is excluded.
var KnownTags = []string{
	"Result",
	"Insert",
	"Query",
	"Edit",
	"Cell Edit",
	"Visual",
	"Transaction",
	"Commit",
	"Help",
	"Operator",
	"Edit history",
	"Conflict",
	"Text object",
	"Session",
	"Row",
	"Motion",
	"Connection",
	"Plan",
}
