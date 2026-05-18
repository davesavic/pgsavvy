package orchestrator_test

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

func TestRunLayoutGatesLimitOverlay(t *testing.T) {
	g, rec := buildTestGui(t)
	// Below threshold → only the LIMIT view is laid out.
	if err := g.RunLayout(5, 5); err != nil {
		t.Fatalf("RunLayout small: %v", err)
	}
	if !rec.HasSetView(string(types.LIMIT)) {
		t.Fatal("expected LIMIT view to be created at 5x5")
	}
	for _, name := range []string{
		string(types.CONNECTIONS),
		string(types.SCHEMAS),
		string(types.TABLES),
	} {
		if rec.HasSetView(name) {
			t.Errorf("did not expect SetView(%q) on tiny terminal", name)
		}
	}
}

func TestRunLayoutSkipsStubContexts(t *testing.T) {
	g, rec := buildTestGui(t)
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout large: %v", err)
	}
	for _, name := range []string{
		string(types.QUERY_EDITOR),
		string(types.TABLE_DATA_EDITOR),
		string(types.RESULT_GRID),
		string(types.PLAN),
		string(types.WHICH_KEY),
		string(types.HISTORY),
	} {
		if rec.HasSetView(name) {
			t.Errorf("stub context %q must not be laid out", name)
		}
	}
}

func TestRunLayoutCreatesSideRails(t *testing.T) {
	g, rec := buildTestGui(t)
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	for _, name := range []string{
		string(types.CONNECTIONS),
		string(types.SCHEMAS),
		string(types.TABLES),
		string(types.COLUMNS),
		string(types.INDEXES),
	} {
		if !rec.HasSetView(name) {
			t.Errorf("side rail %q not laid out at 120x40", name)
		}
	}
}

// TestRunLayoutNoQueuedWriteErrors regresses the startup crash where
// LimitContext.HandleRender ran in the normal-size flatten pass and
// queued a Write to the "limit" view, which the layout never created.
// Real gocui surfaces that ErrUnknownView out of the MainLoop and kills
// the TUI on the first frame; the RecorderGuiDriver previously dropped
// the error silently, so this assertion locks the behaviour in.
func TestRunLayoutNoQueuedWriteErrors(t *testing.T) {
	g, rec := buildTestGui(t)
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	if errs := rec.UpdateErrors(); len(errs) > 0 {
		for _, e := range errs {
			t.Errorf("queued Update closure returned error: %v", e)
		}
	}
}

// TestRunLayoutOmitsOffStackPopups asserts the per-Kind dispatch
// contract: popup contexts (MENU/CONFIRMATION/PROMPT/SUGGESTIONS/
// COMMAND_LINE/CHEATSHEET) must NOT have SetView called when they are
// absent from the focus stack — otherwise empty popup rectangles
// occlude the screen under gocui.SupportOverlaps=false.
func TestRunLayoutOmitsOffStackPopups(t *testing.T) {
	g, rec := buildTestGui(t)
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	for _, name := range []string{
		string(types.MENU),
		string(types.CONFIRMATION),
		string(types.PROMPT),
		string(types.SUGGESTIONS),
		string(types.COMMAND_LINE),
		string(types.CHEATSHEET),
	} {
		if rec.HasSetView(name) {
			t.Errorf("popup %q must not be laid out when off the focus stack", name)
		}
	}
}

// TestRunLayoutCreatesPopupOnStack pushes MENU onto the focus stack
// and asserts the Tier-3 popup pass creates the view. After Pop, the
// next RunLayout pass must DeleteView the now-orphan popup.
func TestRunLayoutCreatesPopupOnStack(t *testing.T) {
	g, rec := buildTestGui(t)
	menu := g.Registry().Menu
	if menu == nil {
		t.Fatal("registry.Menu is nil")
	}
	if err := g.ContextTree().Push(menu); err != nil {
		t.Fatalf("Push(menu): %v", err)
	}
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout post-push: %v", err)
	}
	if !rec.HasSetView(string(types.MENU)) {
		t.Fatal("MENU SetView not invoked after Push")
	}

	if err := g.ContextTree().Pop(); err != nil {
		t.Fatalf("Pop: %v", err)
	}
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout post-pop: %v", err)
	}
	found := false
	for _, name := range rec.DeleteViews {
		if name == string(types.MENU) {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("MENU DeleteView not invoked after Pop")
	}
}
