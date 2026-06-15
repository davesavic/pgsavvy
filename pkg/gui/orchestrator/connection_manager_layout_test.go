package orchestrator_test

import (
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// TestRunLayoutConnectionManagerOwnsMainPane: when the
// CONNECTION_MANAGER MAIN_CONTEXT is top of the focus stack it renders a
// centered bordered box over a blank background — the side rails AND the
// QUERY_EDITOR paint must be suppressed for that frame so nothing renders
// behind the modal.
func TestRunLayoutConnectionManagerOwnsMainPane(t *testing.T) {
	g, rec := buildTestGui(t)
	cm := g.Registry().ConnectionManager
	if cm == nil {
		t.Fatal("registry.ConnectionManager is nil")
	}
	if err := g.ContextTree().Push(cm); err != nil {
		t.Fatalf("Push(connection_manager): %v", err)
	}
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}

	if !rec.HasSetView(string(types.CONNECTION_MANAGER)) {
		t.Fatal("CONNECTION_MANAGER SetView not invoked; modal would be invisible")
	}
	if rec.HasSetView(string(types.QUERY_EDITOR)) {
		t.Fatal("QUERY_EDITOR painted while CONNECTION_MANAGER is top; must be suppressed")
	}
	// Side rails must not paint behind the modal.
	for _, name := range []string{
		string(types.SCHEMAS),
		string(types.SCHEMAS),
		string(types.TABLES),
	} {
		if rec.HasSetView(name) {
			t.Errorf("side rail %q painted while CONNECTION_MANAGER is top; must be suppressed", name)
		}
	}

	// The placeholder body renders without panic.
	if got := rec.GetViewBuffer(string(types.CONNECTION_MANAGER)); got == "" {
		t.Fatal("CONNECTION_MANAGER view content is empty; placeholder body not rendered")
	}
}

// TestRunLayoutConnectionManagerCenteredBox asserts the modal SetView rect
// is a centered sub-rect of dims["main"], not the full pane.
func TestRunLayoutConnectionManagerCenteredBox(t *testing.T) {
	g, rec := buildTestGui(t)
	cm := g.Registry().ConnectionManager
	if cm == nil {
		t.Fatal("registry.ConnectionManager is nil")
	}
	if err := g.ContextTree().Push(cm); err != nil {
		t.Fatalf("Push(connection_manager): %v", err)
	}
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}

	var found bool
	for _, c := range rec.AllSetViewCalls() {
		if c.Name != string(types.CONNECTION_MANAGER) {
			continue
		}
		found = true
		// The box must be inset from the screen edges (centered), not
		// pinned at (0,0) or stretched to the full 120x40 canvas.
		if c.X0 <= 0 || c.Y0 <= 0 {
			t.Errorf("CONNECTION_MANAGER rect not inset from top-left: %+v", c)
		}
		if c.X1 >= 119 || c.Y1 >= 39 {
			t.Errorf("CONNECTION_MANAGER rect not inset from bottom-right: %+v", c)
		}
	}
	if !found {
		t.Fatal("no SetView call recorded for CONNECTION_MANAGER")
	}
}

// TestConnectionManagerRendersWithoutConnections asserts the placeholder
// body renders without panic when there are zero connections.
func TestConnectionManagerRendersWithoutConnections(t *testing.T) {
	g, rec := buildTestGui(t)
	cm := g.Registry().ConnectionManager
	if cm == nil {
		t.Fatal("registry.ConnectionManager is nil")
	}
	if err := g.ContextTree().Push(cm); err != nil {
		t.Fatalf("Push(connection_manager): %v", err)
	}
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	if got := rec.GetViewBuffer(string(types.CONNECTION_MANAGER)); strings.TrimSpace(got) == "" {
		t.Fatal("placeholder body empty with zero connections")
	}
}

// TestConnectionManagerRootExitNeverPopsBottom asserts the root-exit
// invariant the modal's <esc> close path relies on: at stack depth 1 the
// focus stack's Pop is a guarded no-op — it returns ErrPopAtBottom and leaves
// the stack unchanged, so Esc at the startup root never drops the final
// entry. Pushing then popping the modal returns the stack to its original
// single-entry root and confirms the bottom guard holds.
func TestConnectionManagerRootExitNeverPopsBottom(t *testing.T) {
	g, _ := buildTestGui(t)
	cm := g.Registry().ConnectionManager
	if cm == nil {
		t.Fatal("registry.ConnectionManager is nil")
	}
	tree := g.ContextTree()

	// Push the modal so it is the top, then drain back down to the single
	// root entry (the modal and any seeded contexts pop off cleanly).
	if err := tree.Push(cm); err != nil {
		t.Fatalf("Push(connection_manager): %v", err)
	}
	for len(tree.Stack()) > 1 {
		if err := tree.Pop(); err != nil {
			t.Fatalf("drain Pop: %v", err)
		}
	}
	if got := len(tree.Stack()); got != 1 {
		t.Fatalf("expected single-entry stack after drain, got depth %d", got)
	}

	// At depth 1 Pop must be a guarded no-op (ErrPopAtBottom, stack
	// unchanged) — the invariant the modal's Esc-at-root close relies on.
	if err := tree.Pop(); err == nil {
		t.Fatal("Pop at stack bottom returned nil; expected ErrPopAtBottom guard")
	}
	if got := len(tree.Stack()); got != 1 {
		t.Fatalf("stack changed after guarded Pop: depth %d, want 1", got)
	}
}
