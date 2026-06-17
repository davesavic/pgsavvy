package testfake

import (
	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// ExpectedBinding is one (view, key, mod) tuple the bootstrap is expected
// to register. The list is the union of every controller-published
// binding's top-level (first-chord) key, since per-key SetKeybinding
// shims are installed only for the trie's root children — the Matcher
// then takes over for any subsequent keys in a multi-key chord.
type ExpectedBinding struct {
	View string
	Key  types.Key
	Mod  types.Modifier
}

// ExpectedBindings is the canonical inventory the AC suite walks.
//
// Coverage targets: j, k, <CR>, H, U, leader, digit 1..4, <tab>,
// ?, <c-c>, :, a — across every relevant view.
var ExpectedBindings = []ExpectedBinding{
	// NOTE (pgsavvy-i42s.4): the SCHEMAS/TABLES side rails were consolidated
	// into the single SCHEMA_RAIL container view "schemas-tables", which
	// dispatches every keystroke through its master editor (like
	// QUERY_EDITOR/RESULT_GRID) under the FIXED scope SCHEMA_RAIL — so the
	// rails no longer receive per-key SetKeybinding shims on "schemas"/"tables".
	// Republishing the rail nav/leader bindings under the SCHEMA_RAIL scope is
	// pgsavvy-i42s.5; the per-rail-view entries below were removed when the
	// views were retired. Only the always-on Esc-abort + emergency-quit shims
	// remain on "schemas-tables" (covered by whichkey_esc_abort_test.go).

	// Menu popup bindings.
	{View: "menu", Key: gocui.NewKeyName(gocui.KeyEnter), Mod: gocui.ModNone},
	{View: "menu", Key: gocui.NewKeyName(gocui.KeyEsc), Mod: gocui.ModNone},

	// Global bindings (view == "" means global per gocui).
	{View: "", Key: gocui.NewKeyRune('c'), Mod: gocui.ModCtrl},
	{View: "", Key: gocui.NewKeyRune(':'), Mod: gocui.ModNone},
	{View: "", Key: gocui.NewKeyRune('?'), Mod: gocui.ModNone},
}
