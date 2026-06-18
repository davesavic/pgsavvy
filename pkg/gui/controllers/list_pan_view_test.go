package controllers

import "testing"

// viewNamedCursor is a SideListCursor that also reports the view it renders
// into — the shape the live leaf contexts (SideListContext/HistoryContext/…)
// satisfy via the promoted BaseContext.GetViewName.
type viewNamedCursor struct{ view string }

func (viewNamedCursor) Cursor() int           { return 0 }
func (viewNamedCursor) SetCursor(int)         {}
func (viewNamedCursor) Items() []any          { return nil }
func (c viewNamedCursor) GetViewName() string { return c.view }

// Regression for the tabbed-rail refactor: both SCHEMA_RAIL leaves render into
// the single consolidated "schemas-tables" view, so the rail must scroll THAT
// view, not the leaf's dispatch identity ("schemas"). Before the fix railView
// looked up l.viewName ("schemas") — a view that no longer exists — so every
// h/l/0/$ pan silently no-oped. railViewName now tracks the context's view.
func TestRailViewNameTracksContextView(t *testing.T) {
	l := &ListControllerTrait[int]{
		viewName: "schemas",
		cursor:   viewNamedCursor{view: "schemas-tables"},
	}
	if got := l.railViewName(); got != "schemas-tables" {
		t.Fatalf("railViewName() = %q, want %q (the view the context renders into)", got, "schemas-tables")
	}
}

// A cursor that exposes no view (test fakes) or a nil cursor falls back to the
// dispatch identity so non-tabbed callers keep their original target.
func TestRailViewNameFallsBackToDispatchIdentity(t *testing.T) {
	l := &ListControllerTrait[int]{viewName: "schemas"}
	if got := l.railViewName(); got != "schemas" {
		t.Fatalf("railViewName() with nil cursor = %q, want %q", got, "schemas")
	}
}
