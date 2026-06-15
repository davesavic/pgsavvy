package ui_test

// Seam-level flow tests for the rail highlight+jump search.
//
// The orchestrator's openRailSearch / setRailMatchCount are unexported
// methods on *Gui and wiring a full Gui in a unit test is impractical, so
// these tests exercise the exact SearchLineHelper opts the orchestrator
// builds against a real *context.SideListContext. The opts here mirror
// gui.go:openRailSearch one-for-one (OnChange→SetSearch + count refresh,
// OnAccept land-only no-op, OnCancel→ClearSearch, CursorSnapshot/Restore
// via Cursor/SetCursor). railMatchCount mirrors setRailMatchCount.

import (
	"fmt"
	"testing"

	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

type railRow struct{ name string }

func railName(v any) string { return v.(*railRow).name }

func newRail(names ...string) *guicontext.SideListContext {
	base := guicontext.NewBaseContext(guicontext.BaseContextOpts{
		Key:      types.TABLES,
		ViewName: string(types.TABLES),
		Kind:     types.SIDE_CONTEXT,
	})
	c := guicontext.NewSideListContext(base, guicontext.Deps{})
	c.SetRailNameAccessor(railName)
	items := make([]any, len(names))
	for i, n := range names {
		items[i] = &railRow{name: n}
	}
	c.SetItems(items)
	return &c
}

// railMatchCount mirrors gui.go:setRailMatchCount — the exact string the
// SearchLine count slot receives.
func railMatchCount(rail *guicontext.SideListContext) string {
	_, cur, total, active := rail.SearchStatus()
	if !active {
		return ""
	}
	return fmt.Sprintf("%d/%d", cur, total)
}

// openRailSearch mirrors gui.go:openRailSearch exactly. lastCount is set
// by every count refresh so the test can observe the slot value.
func openRailSearch(h *ui.SearchLineHelper, rail *guicontext.SideListContext, lastCount *string) error {
	refresh := func() {
		c := railMatchCount(rail)
		*lastCount = c
		h.SetMatchCount(c)
	}
	return h.Open(ui.SearchLineOpts{
		OnChange: func(query string) {
			rail.SetSearch(query)
			refresh()
		},
		OnAccept:       func(string) {},
		OnCancel:       func() { rail.ClearSearch() },
		CursorSnapshot: func() any { return rail.Cursor() },
		CursorRestore: func(snap any) {
			if i, ok := snap.(int); ok {
				rail.SetCursor(i)
			}
		},
	})
}

// AC (passthrough flow): firing the SEARCH_LINE passthrough (OnChange)
// drives the focused rail's SetSearch with the typed query; the cursor
// lands on the first match and SearchActive becomes true.
func TestRailSearch_OnChangeDrivesSetSearch(t *testing.T) {
	rail := newRail("audit", "users", "user_roles")
	var count string
	h := ui.NewSearchLineHelper(nil, nil)
	if err := openRailSearch(h, rail, &count); err != nil {
		t.Fatalf("open: %v", err)
	}

	h.OnChange("user")

	if !rail.SearchActive() {
		t.Fatal("SearchActive = false after OnChange; want true")
	}
	if rail.Cursor() != 1 {
		t.Fatalf("cursor = %d after search; want 1 (first match row)", rail.Cursor())
	}
	if count != "1/2" {
		t.Fatalf("count slot = %q; want 1/2", count)
	}
}

// AC (count): editing back to an empty query clears the count slot.
func TestRailSearch_EmptyQueryClearsCount(t *testing.T) {
	rail := newRail("users", "audit")
	var count string
	h := ui.NewSearchLineHelper(nil, nil)
	_ = openRailSearch(h, rail, &count)

	h.OnChange("user")
	if count == "" {
		t.Fatal("count empty after non-empty query")
	}
	h.OnChange("")
	if count != "" {
		t.Fatalf("count slot = %q after empty query; want empty", count)
	}
	if rail.SearchActive() {
		t.Fatal("SearchActive = true after empty query; want false")
	}
}

// AC (<cr> land-only): OnAccept keeps the search active and the cursor on
// the landed match (OnAccept is a no-op; no ClearSearch, no restore).
func TestRailSearch_AcceptIsLandOnly(t *testing.T) {
	rail := newRail("audit", "users", "user_roles")
	var count string
	h := ui.NewSearchLineHelper(nil, nil)
	_ = openRailSearch(h, rail, &count)

	h.OnChange("user")
	landed := rail.Cursor()
	if err := h.OnAccept("user"); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if !rail.SearchActive() {
		t.Fatal("SearchActive = false after <cr>; want true (land-only)")
	}
	if rail.Cursor() != landed {
		t.Fatalf("cursor moved on <cr>: %d != %d", rail.Cursor(), landed)
	}
}

// AC (<esc> cancel): OnCancel restores the pre-search cursor and clears
// the search (highlight gone).
func TestRailSearch_CancelRestoresAndClears(t *testing.T) {
	rail := newRail("audit", "users", "user_roles")
	rail.SetCursor(0)
	var count string
	h := ui.NewSearchLineHelper(nil, nil)
	_ = openRailSearch(h, rail, &count)

	h.OnChange("user") // cursor jumps to row 1
	if rail.Cursor() != 1 {
		t.Fatalf("precondition: cursor = %d, want 1", rail.Cursor())
	}
	if err := h.OnCancel(); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if rail.SearchActive() {
		t.Fatal("SearchActive = true after <esc>; want false")
	}
	if rail.Cursor() != 0 {
		t.Fatalf("cursor = %d after <esc>; want 0 (pre-search position restored)", rail.Cursor())
	}
}

// AC (per-rail isolation): a search active on one rail does not leak into
// another — separate SideListContexts hold independent search state.
func TestRailSearch_PerRailIsolation(t *testing.T) {
	tables := newRail("users", "user_roles")
	schemas := newRail("public", "audit")

	var c1 string
	h1 := ui.NewSearchLineHelper(nil, nil)
	_ = openRailSearch(h1, tables, &c1)
	h1.OnChange("user")

	if !tables.SearchActive() {
		t.Fatal("tables search not active")
	}
	if schemas.SearchActive() {
		t.Fatal("schemas search active; tables search leaked across rails")
	}
}
