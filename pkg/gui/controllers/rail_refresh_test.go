package controllers_test

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// dbsavvy-56u.1: pressing `r` on the SCHEMAS rail dispatches through
// HelperBag.Refresh.RefreshSchemas. The binding pattern is symmetric
// across the five side rails; SCHEMAS is exercised as the canonical
// rail.
func TestSchemasControllerRBindingDispatchesRefresh(t *testing.T) {
	b := newBag()
	refresh := &fakeRefresh{}
	b.HelperBag.Refresh = refresh

	cur := &fakeCursor{}
	ctrl := controllers.NewSchemasController(nil, b.HelperBag, cur, b.SchemaPicker)
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

// dbsavvy-56u.1: TablesController's `r` handler resolves the active
// schema from HelperBag.Schemas and dispatches RefreshTables with it.
func TestTablesControllerRBindingDispatchesRefreshWithSchema(t *testing.T) {
	b := newBag()
	refresh := &fakeRefresh{}
	b.HelperBag.Refresh = refresh
	b.SchemaPicker.name = "public"

	cur := &fakeCursor{}
	ctrl := controllers.NewTablesController(nil, b.HelperBag, cur, b.TablePicker)
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

// dbsavvy-56u.1: ColumnsController's `r` handler resolves the active
// table from HelperBag.Tables and dispatches RefreshColumns. With no
// table selected (nil picker) the dispatch is a silent no-op.
func TestColumnsControllerRBindingDispatchesRefreshWithTable(t *testing.T) {
	b := newBag()
	refresh := &fakeRefresh{}
	b.HelperBag.Refresh = refresh
	b.TablePicker.sel = &models.Table{Schema: "public", Name: "users"}

	cur := &fakeCursor{}
	ctrl := controllers.NewColumnsController(nil, b.HelperBag, cur)
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
	if len(refresh.columns) != 1 || refresh.columns[0] != (refreshTC{"public", "users"}) {
		t.Fatalf("RefreshColumns calls = %+v, want one (public, users)", refresh.columns)
	}
}

// dbsavvy-56u.1: ColumnsController.RefreshRail with no selected table
// must NOT dispatch (stale-guard at the boundary so the helper never
// receives empty identifiers).
func TestColumnsControllerRBindingWithNoTableIsNoop(t *testing.T) {
	b := newBag()
	refresh := &fakeRefresh{}
	b.HelperBag.Refresh = refresh
	// b.TablePicker.sel intentionally nil.

	cur := &fakeCursor{}
	ctrl := controllers.NewColumnsController(nil, b.HelperBag, cur)
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
	if len(refresh.columns) != 0 {
		t.Fatalf("RefreshColumns fired with no selected table: %+v", refresh.columns)
	}
}

// dbsavvy-56u.1: ConnectionsController's `r` handler dispatches
// RefreshConnections.
func TestConnectionsControllerRBindingDispatchesRefresh(t *testing.T) {
	b := newBag()
	refresh := &fakeRefresh{}
	b.HelperBag.Refresh = refresh

	cur := &fakeCursor{}
	ctrl := controllers.NewConnectionsController(nil, b.HelperBag, cur, b.ConnPicker)
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
	if refresh.connections != 1 {
		t.Fatalf("RefreshConnections calls = %d, want 1", refresh.connections)
	}
}
