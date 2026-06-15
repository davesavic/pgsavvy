package controllers_test

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// pressing `r` on the SCHEMAS rail dispatches through
// HelperBag.Refresh.RefreshSchemas. The binding pattern is symmetric
// across the three side rails; SCHEMAS is exercised as the canonical
// rail.
func TestSchemasControllerRBindingDispatchesRefresh(t *testing.T) {
	b := newBag()
	refresh := &fakeRefresh{}
	b.HelperBag.Refresh = refresh

	cur := &fakeCursor{}
	ctrl := controllers.NewSchemasController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, b.HelperBag.UIDeps, cur, b.SchemaPicker)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	var found bool
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if !isRune(kb, 'r') {
			continue
		}
		found = true
		if kb.Scope != types.SCHEMAS {
			t.Errorf("r binding scope = %q, want SCHEMAS", kb.Scope)
		}
		if err := invokeAction(reg, kb); err != nil {
			t.Fatalf("r: %v", err)
		}
	}
	if !found {
		t.Fatal("SchemasController.GetKeybindings: no `r` binding emitted")
	}
	if refresh.schemas != 1 {
		t.Fatalf("RefreshSchemas calls = %d, want 1", refresh.schemas)
	}
}

// TablesController's `r` handler resolves the active
// schema from HelperBag.Schemas and dispatches RefreshTables with it.
func TestTablesControllerRBindingDispatchesRefreshWithSchema(t *testing.T) {
	b := newBag()
	refresh := &fakeRefresh{}
	b.HelperBag.Refresh = refresh
	b.SchemaPicker.name = "public"

	cur := &fakeCursor{}
	ctrl := controllers.NewTablesController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, cur, b.TablePicker)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if !isRune(kb, 'r') {
			continue
		}
		if err := invokeAction(reg, kb); err != nil {
			t.Fatalf("r: %v", err)
		}
	}
	if len(refresh.tables) != 1 || refresh.tables[0] != "public" {
		t.Fatalf("RefreshTables calls = %v, want [public]", refresh.tables)
	}
}
