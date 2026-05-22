package controllers_test

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// TestListControllerTrait_PerRailDispatch guards dbsavvy-6m9: j/k on the
// SCHEMAS (or TABLES) rail must move THAT rail's cursor, not the
// CONNECTIONS rail's cursor. Before the fix every rail emitted bindings
// with a single shared ActionID (commands.ListDown / ListUp) and only
// the Connections trait registered handlers — so j on SCHEMAS moved the
// Connections cursor through the global ActionID.
//
// The test wires every rail-cursor with its own fakeCursor, has every
// rail register its trait actions on the same Registry, then fires
// the SCHEMAS rail's j binding and asserts only the SCHEMAS cursor
// advanced.
func TestListControllerTrait_PerRailDispatch(t *testing.T) {
	b := newBag()
	connCur := &fakeCursor{items: []any{1, 2, 3}}
	schemaCur := &fakeCursor{items: []any{1, 2, 3}}
	tableCur := &fakeCursor{items: []any{1, 2, 3}}

	conn := controllers.NewConnectionsController(nil, b.HelperBag, connCur, b.ConnPicker)
	schemas := controllers.NewSchemasController(nil, b.HelperBag, schemaCur, b.SchemaPicker)
	tables := controllers.NewTablesController(nil, b.HelperBag, tableCur, b.TablePicker)

	reg := commands.NewRegistry()
	conn.ListControllerTrait.RegisterActions(reg)
	schemas.ListControllerTrait.RegisterActions(reg)
	tables.ListControllerTrait.RegisterActions(reg)

	// Fire SCHEMAS rail's j binding. Only schemaCur must advance.
	fireJOn(t, reg, schemas.GetKeybindings(types.KeybindingsOpts{}), "schemas")
	if schemaCur.idx != 1 {
		t.Errorf("schemas cursor after j = %d, want 1", schemaCur.idx)
	}
	if connCur.idx != 0 {
		t.Errorf("connections cursor moved to %d after j on SCHEMAS — should stay 0", connCur.idx)
	}
	if tableCur.idx != 0 {
		t.Errorf("non-target rail moved: tables=%d, want 0", tableCur.idx)
	}

	// Symmetric check on TABLES.
	fireJOn(t, reg, tables.GetKeybindings(types.KeybindingsOpts{}), "tables")
	if tableCur.idx != 1 {
		t.Errorf("tables cursor after j = %d, want 1", tableCur.idx)
	}
	if connCur.idx != 0 {
		t.Errorf("connections cursor moved to %d after j on TABLES — should stay 0", connCur.idx)
	}

	// And CONNECTIONS itself still works.
	fireJOn(t, reg, conn.GetKeybindings(types.KeybindingsOpts{}), "connections")
	if connCur.idx != 1 {
		t.Errorf("connections cursor after j = %d, want 1", connCur.idx)
	}
}

// fireJOn finds the bare-'j' binding in bindings and dispatches it
// through reg. Fails the test if no 'j' is found.
func fireJOn(t *testing.T, reg *commands.Registry, bindings []*types.ChordBinding, label string) {
	t.Helper()
	for _, kb := range bindings {
		if isRune(kb, 'j') {
			if err := invokeAction(reg, kb); err != nil {
				t.Fatalf("invoke j on %s: %v", label, err)
			}
			return
		}
	}
	t.Fatalf("no 'j' binding found for %s rail", label)
}
