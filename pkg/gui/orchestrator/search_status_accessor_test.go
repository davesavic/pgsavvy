package orchestrator

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// newSearchAccessorFixture builds a Gui whose active result tab holds a
// grid with an installed search, plus a focus tree seeded at root. The
// grid is loaded with three "apple" cells so SearchStatus reports a
// non-trivial count, isolating the accessor's focus gate from the
// engine's match counting.
func newSearchAccessorFixture(t *testing.T, root types.ContextKey) (*Gui, *gui.ContextTree) {
	t.Helper()
	tabs := ui.NewResultTabsHelper(ui.ResultTabsHelperDeps{
		StreamFactory: func() ui.StreamRunner { return &fkStopRunner{} },
	})
	if err := tabs.OpenResultTab("t", nil); err != nil {
		t.Fatalf("OpenResultTab: %v", err)
	}
	grid := tabs.Active().Grid()
	if grid == nil {
		t.Fatal("active tab has no grid")
	}
	grid.SetColumns([]models.ColumnMeta{{Name: "fruit"}})
	grid.AppendRows([]models.Row{
		{Values: []any{"apple"}},
		{Values: []any{"banana"}},
		{Values: []any{"apple"}},
		{Values: []any{"apple"}},
	})
	grid.SetSearch("apple")

	tree := seedTree(t, root)
	return &Gui{resultTabsH: tabs, tree: tree}, tree
}

// TestSearchStatusAccessorActiveOnResultGrid is the regression guard for
// Every result tab's focus-stack context carries
// GetKey() == RESULT_GRID (the per-slot result_tab_<slot> name lives on
// the VIEW, not the key). The accessor must gate on the RESULT_GRID key,
// not the result_tab_ view prefix — gating on the prefix made the
// active-search status segment never render. With the result grid
// focused and a live search, the accessor must surface it.
func TestSearchStatusAccessorActiveOnResultGrid(t *testing.T) {
	g, _ := newSearchAccessorFixture(t, types.RESULT_GRID)

	accessor := g.searchStatusAccessor()
	if accessor == nil {
		t.Fatal("searchStatusAccessor returned nil with a wired resultTabsH")
	}

	query, cur, total, active := accessor()
	if !active {
		t.Fatal("accessor reported inactive while the result grid is focused with a live search")
	}
	if query != "apple" {
		t.Errorf("query = %q, want %q", query, "apple")
	}
	if total != 3 {
		t.Errorf("total = %d, want 3 (three apple cells)", total)
	}
	if cur != 1 {
		t.Errorf("cur = %d, want 1 (first match)", cur)
	}
}

// TestSearchStatusAccessorInactiveOffResultGrid proves the segment
// clears when focus is not the result grid, even though the search is
// still live on the tab's grid — the indicator tracks focus, not the
// existence of a search.
func TestSearchStatusAccessorInactiveOffResultGrid(t *testing.T) {
	g, tree := newSearchAccessorFixture(t, types.RESULT_GRID)
	if err := tree.Replace(newSwapHookContext(types.QUERY_EDITOR, types.MAIN_CONTEXT)); err != nil {
		t.Fatalf("Replace(QUERY_EDITOR): %v", err)
	}

	_, _, _, active := g.searchStatusAccessor()()
	if active {
		t.Fatal("accessor reported active while focus is on QUERY_EDITOR, not the result grid")
	}
}
