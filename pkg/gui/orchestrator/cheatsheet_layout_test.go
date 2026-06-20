package orchestrator_test

import (
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/pgsavvy/pkg/theme"
)

// TestCheatsheetTabColorsApplied is the regression guard for the cheatsheet
// active-tab colour seam: after the `?` cheatsheet popup is opened and a real
// layout frame runs, the orchestrator must apply the native active/inactive tab
// colours to the CHEATSHEET container view — mirroring SCHEMA_RAIL/QUERY_RAIL/
// TABLE_INSPECT. Without the SetViewTabColors call the active tab carries only
// its "[...]" bracket marker and renders in the same foreground as the inactive
// tabs (no active-title highlight). The active tab gets ActiveBorder via
// SelFgColor; the inactive colour is ColorDefault so the leaf body is not dimmed.
func TestCheatsheetTabColorsApplied(t *testing.T) {
	g, rec := buildTestGui(t)

	// Open the cheatsheet the production way: the HelpCheatsheet handler builds
	// the per-Category tabs and pushes the popup onto the focus stack.
	cmd, ok := g.CommandRegistry().Get(commands.HelpCheatsheet)
	if !ok || cmd == nil || cmd.Handler == nil {
		t.Fatalf("HelpCheatsheet not registered or handler nil")
	}
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("HelpCheatsheet handler: %v", err)
	}

	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}

	viewName := g.Registry().Cheatsheet.GetViewName()

	wantActive := themeFrameAttr(theme.Current().ActiveBorder)
	wantInactive := gocui.ColorDefault
	var lastColors *testfake.SetViewTabColorsCall
	for i := range rec.AllSetViewTabColorsCalls() {
		c := rec.AllSetViewTabColorsCalls()[i]
		if c.Name == viewName {
			lastColors = &c
		}
	}
	if lastColors == nil {
		t.Fatalf("no SetViewTabColors call for cheatsheet view %q; active tab has no colour highlight", viewName)
	}
	if lastColors.ActiveFg != wantActive || lastColors.InactiveFg != wantInactive {
		t.Errorf("cheatsheet tab colours = (active %v, inactive %v), want (active %v, inactive %v)",
			lastColors.ActiveFg, lastColors.InactiveFg, wantActive, wantInactive)
	}
}
