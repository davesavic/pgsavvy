package orchestrator_test

import (
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// TestFirstRunTipAddKeyDismissesAndOpensForm asserts the tip's "Press a to
// add your first connection" copy actually works: pressing `a` while
// FIRST_RUN_TIP owns input dismisses the tip and lands the connection manager
// in its add form. The connection-manager `a` binding is
// CONNECTION_MANAGER-scoped, so without the tip-view shim it never fires while
// the tip is on top — the user-reported "press a does nothing".
func TestFirstRunTipAddKeyDismissesAndOpensForm(t *testing.T) {
	g, rec := buildTestGui(t)
	tree := g.ContextTree()
	// buildTestGui wires with an empty ConnectionsProvider, so startup pushes
	// CONNECTION_MANAGER then FIRST_RUN_TIP on top.
	if got := tree.Current().GetKey(); got != types.FIRST_RUN_TIP {
		t.Fatalf("focus stack top = %q, want %q (precondition)", got, types.FIRST_RUN_TIP)
	}

	if err := rec.FeedKey(string(types.FIRST_RUN_TIP), gocui.NewKeyRune('a'), gocui.ModNone); err != nil {
		t.Fatalf("FeedKey `a` on first_run_tip: %v (binding not registered?)", err)
	}

	if got := tree.Current().GetKey(); got != types.CONNECTION_MANAGER {
		t.Fatalf("after `a`: focus top = %q, want %q (tip should be dismissed)", got, types.CONNECTION_MANAGER)
	}
	if got := g.Registry().ConnectionManager.Mode(); got != guicontext.ModeForm {
		t.Fatalf("after `a`: connection-manager mode = %v, want ModeForm (add form should open)", got)
	}
}

// TestFirstRunTipHelpKeyDismissesAndOpensCheatsheet asserts the tip's "Press
// ? at any time to see available keys" copy works: pressing `?` while
// FIRST_RUN_TIP owns input dismisses the tip and opens the cheatsheet. `?` is
// a GLOBAL chord that the trie never routes to the popup view, so it needs the
// tip-view shim.
func TestFirstRunTipHelpKeyDismissesAndOpensCheatsheet(t *testing.T) {
	g, rec := buildTestGui(t)
	tree := g.ContextTree()
	if got := tree.Current().GetKey(); got != types.FIRST_RUN_TIP {
		t.Fatalf("focus stack top = %q, want %q (precondition)", got, types.FIRST_RUN_TIP)
	}

	if err := rec.FeedKey(string(types.FIRST_RUN_TIP), gocui.NewKeyRune('?'), gocui.ModNone); err != nil {
		t.Fatalf("FeedKey `?` on first_run_tip: %v (binding not registered?)", err)
	}

	if got := tree.Current().GetKey(); got != types.CHEATSHEET {
		t.Fatalf("after `?`: focus top = %q, want %q (tip dismissed, cheatsheet opened)", got, types.CHEATSHEET)
	}
}

// TestShowTipExCommandRePushesTip asserts the `:tip` ex-command re-displays
// the welcome tip after it's been dismissed, so it can be re-checked on
// demand independent of the startup seen-stamp gate.
func TestShowTipExCommandRePushesTip(t *testing.T) {
	g, rec := buildTestGui(t)
	tree := g.ContextTree()

	// Dismiss the boot-time tip so it's no longer showing.
	if err := rec.FeedKey(string(types.FIRST_RUN_TIP), gocui.NewKeyName(gocui.KeyEnter), gocui.ModNone); err != nil {
		t.Fatalf("FeedKey Enter to dismiss tip: %v", err)
	}
	if got := tree.Current().GetKey(); got == types.FIRST_RUN_TIP {
		t.Fatal("precondition: tip still top after dismiss")
	}

	cmd, ok := g.ExRegistry().Get("tip")
	if !ok {
		t.Fatal(":tip ex-command not registered")
	}
	if err := cmd.Handler(nil, commands.ExecCtx{}); err != nil {
		t.Fatalf(":tip handler: %v", err)
	}

	if got := tree.Current().GetKey(); got != types.FIRST_RUN_TIP {
		t.Fatalf("after :tip: focus top = %q, want %q (tip re-shown)", got, types.FIRST_RUN_TIP)
	}
}
