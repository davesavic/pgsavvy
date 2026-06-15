package orchestrator

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// newConflictHookFixture builds a tabs helper whose active grid renders
// an id+data projection with row identity on id, plus a staged-edit set
// holding one conflicted edit per supplied column. Streaming is disabled
// (nil StreamFactory) — the hooks only touch tab/grid state.
func newConflictHookFixture(t *testing.T, row models.Row, cols ...string) (*ui.ResultTabsHelper, *models.PendingEditSet) {
	t.Helper()
	tabs := ui.NewResultTabsHelper(ui.ResultTabsHelperDeps{})
	if err := tabs.OpenResultTab("t", nil); err != nil {
		t.Fatalf("OpenResultTab: %v", err)
	}
	g := tabs.Active().Grid()
	if g == nil {
		t.Fatal("active tab has no grid")
	}
	meta := make([]models.ColumnMeta, len(cols))
	for i, c := range cols {
		meta[i] = models.ColumnMeta{Name: c}
	}
	g.SetColumns(meta)
	g.SetEditability(true, []int{0}, "", "app")
	g.AppendRows([]models.Row{row})
	return tabs, &models.PendingEditSet{}
}

// TestConflictRefreshHookWritesServerValueToGrid is the regression guard:
// `[r]` must surface the conflict-time server value in
// the grid instead of leaving the stale loaded value rendered once the
// dirty-cell substitution disappears with the dropped edit.
func TestConflictRefreshHookWritesServerValueToGrid(t *testing.T) {
	tabs, set := newConflictHookFixture(t,
		models.Row{Values: []any{int64(2), "loaded"}}, "id", "data")

	edit := models.PendingEdit{
		PrimaryKey: []any{int64(2)},
		Column:     "data",
		OldValue:   "loaded",
		NewValue:   "mine",
	}
	if err := set.Add(edit); err != nil {
		t.Fatalf("Add: %v", err)
	}

	hook := conflictRefreshHook{deps: conflictDialogDeps{
		tabs:          tabs,
		activeSetFunc: func() *models.PendingEditSet { return set },
	}}
	conflicts := []models.ConflictedEdit{{Edit: edit, ServerValue: "server-drift"}}
	if err := hook.Refresh(conflicts, nil); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if set.HasEdit(edit.PrimaryKey, "data") {
		t.Error("conflicted edit was not dropped from the staged set")
	}
	got := tabs.Active().Grid().AllRows()[0].Values[1]
	if got != "server-drift" {
		t.Errorf("grid cell = %v, want server-drift (stale loaded value still rendered)", got)
	}
}

// TestConflictRefreshHookComposesMultipleColumnsSameRow guards the
// per-conflict write-back when two conflicted columns share one row —
// the second update must not clobber the first.
func TestConflictRefreshHookComposesMultipleColumnsSameRow(t *testing.T) {
	tabs, set := newConflictHookFixture(t,
		models.Row{Values: []any{int64(1), "a-loaded", "b-loaded"}}, "id", "a", "b")

	editA := models.PendingEdit{PrimaryKey: []any{int64(1)}, Column: "a", OldValue: "a-loaded", NewValue: "a-mine"}
	editB := models.PendingEdit{PrimaryKey: []any{int64(1)}, Column: "b", OldValue: "b-loaded", NewValue: "b-mine"}
	for _, e := range []models.PendingEdit{editA, editB} {
		if err := set.Add(e); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}

	hook := conflictRefreshHook{deps: conflictDialogDeps{
		tabs:          tabs,
		activeSetFunc: func() *models.PendingEditSet { return set },
	}}
	conflicts := []models.ConflictedEdit{
		{Edit: editA, ServerValue: "a-server"},
		{Edit: editB, ServerValue: "b-server"},
	}
	if err := hook.Refresh(conflicts, nil); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	row := tabs.Active().Grid().AllRows()[0]
	if row.Values[1] != "a-server" || row.Values[2] != "b-server" {
		t.Errorf("grid row = %v, want [1 a-server b-server]", row.Values)
	}
}
