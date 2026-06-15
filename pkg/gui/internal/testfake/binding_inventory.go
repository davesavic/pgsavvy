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
	// Schemas rail. <leader>H's first key is space (the configured leader).
	{View: "schemas", Key: gocui.NewKeyRune('j'), Mod: gocui.ModNone},
	{View: "schemas", Key: gocui.NewKeyRune('k'), Mod: gocui.ModNone},
	{View: "schemas", Key: gocui.NewKeyName(gocui.KeyEnter), Mod: gocui.ModNone},
	{View: "schemas", Key: gocui.NewKeyRune('H'), Mod: gocui.ModNone},
	{View: "schemas", Key: gocui.NewKeyRune('U'), Mod: gocui.ModNone},
	{View: "schemas", Key: gocui.NewKeyRune(' '), Mod: gocui.ModNone},
	{View: "schemas", Key: gocui.NewKeyRune('1'), Mod: gocui.ModNone},
	{View: "schemas", Key: gocui.NewKeyRune('2'), Mod: gocui.ModNone},
	{View: "schemas", Key: gocui.NewKeyRune('3'), Mod: gocui.ModNone},
	{View: "schemas", Key: gocui.NewKeyRune('4'), Mod: gocui.ModNone},
	{View: "schemas", Key: gocui.NewKeyName(gocui.KeyTab), Mod: gocui.ModNone},

	// Tables rail.
	{View: "tables", Key: gocui.NewKeyRune('j'), Mod: gocui.ModNone},
	{View: "tables", Key: gocui.NewKeyRune('k'), Mod: gocui.ModNone},
	{View: "tables", Key: gocui.NewKeyName(gocui.KeyEnter), Mod: gocui.ModNone},
	{View: "tables", Key: gocui.NewKeyName(gocui.KeyTab), Mod: gocui.ModNone},

	// Menu popup bindings.
	{View: "menu", Key: gocui.NewKeyName(gocui.KeyEnter), Mod: gocui.ModNone},
	{View: "menu", Key: gocui.NewKeyName(gocui.KeyEsc), Mod: gocui.ModNone},

	// Global bindings (view == "" means global per gocui).
	{View: "", Key: gocui.NewKeyRune('c'), Mod: gocui.ModCtrl},
	{View: "", Key: gocui.NewKeyRune(':'), Mod: gocui.ModNone},
	{View: "", Key: gocui.NewKeyRune('?'), Mod: gocui.ModNone},
}
