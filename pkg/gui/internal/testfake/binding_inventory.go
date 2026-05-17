package testfake

import (
	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// ExpectedBinding is one (view, key, mod) tuple the bootstrap is expected
// to register. The list is the union of every controller-published
// binding plus a handful of global ones; gui_test asserts that every
// entry appears in the recorder's AllKeybindings() set.
type ExpectedBinding struct {
	View string
	Key  types.Key
	Mod  types.Modifier
}

// ExpectedBindings is the canonical inventory the AC suite walks.
//
// Coverage targets called out by the T10 AC: j, k, <CR>, H, U, leader,
// digit 1..4, <tab>, ?, q, :, a — across every relevant view.
var ExpectedBindings = []ExpectedBinding{
	// Connections rail: j/k/<CR>, digits 1..4, <tab>, `a`.
	{View: "connections", Key: gocui.NewKeyRune('j'), Mod: gocui.ModNone},
	{View: "connections", Key: gocui.NewKeyRune('k'), Mod: gocui.ModNone},
	{View: "connections", Key: gocui.NewKeyName(gocui.KeyEnter), Mod: gocui.ModNone},
	{View: "connections", Key: gocui.NewKeyRune('a'), Mod: gocui.ModNone},
	{View: "connections", Key: gocui.NewKeyRune('1'), Mod: gocui.ModNone},
	{View: "connections", Key: gocui.NewKeyRune('2'), Mod: gocui.ModNone},
	{View: "connections", Key: gocui.NewKeyRune('3'), Mod: gocui.ModNone},
	{View: "connections", Key: gocui.NewKeyRune('4'), Mod: gocui.ModNone},
	{View: "connections", Key: gocui.NewKeyName(gocui.KeyTab), Mod: gocui.ModNone},

	// Schemas rail: j/k/<CR>, H, U, leader (space), digits, <tab>.
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

	// Tables rail: j/k/<CR>, digits, <tab>.
	{View: "tables", Key: gocui.NewKeyRune('j'), Mod: gocui.ModNone},
	{View: "tables", Key: gocui.NewKeyRune('k'), Mod: gocui.ModNone},
	{View: "tables", Key: gocui.NewKeyName(gocui.KeyEnter), Mod: gocui.ModNone},
	{View: "tables", Key: gocui.NewKeyName(gocui.KeyTab), Mod: gocui.ModNone},

	// Columns rail.
	{View: "columns", Key: gocui.NewKeyRune('j'), Mod: gocui.ModNone},
	{View: "columns", Key: gocui.NewKeyRune('k'), Mod: gocui.ModNone},

	// Indexes rail.
	{View: "indexes", Key: gocui.NewKeyRune('j'), Mod: gocui.ModNone},
	{View: "indexes", Key: gocui.NewKeyRune('k'), Mod: gocui.ModNone},

	// Menu popup bindings.
	{View: "menu", Key: gocui.NewKeyName(gocui.KeyEnter), Mod: gocui.ModNone},
	{View: "menu", Key: gocui.NewKeyName(gocui.KeyEsc), Mod: gocui.ModNone},

	// Global bindings (view == "" means global per gocui).
	{View: "", Key: gocui.NewKeyRune('q'), Mod: gocui.ModNone},
	{View: "", Key: gocui.NewKeyRune(':'), Mod: gocui.ModNone},
	{View: "", Key: gocui.NewKeyRune('?'), Mod: gocui.ModNone},
}
