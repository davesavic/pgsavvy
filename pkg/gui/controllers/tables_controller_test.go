package controllers_test

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// AC: tables_controller `<CR>` fires HelperBag.OnTableActivate with the
// cursor-selected table.
func TestTablesControllerConfirmFiresOnTableActivate(t *testing.T) {
	b := newBag()
	tbl := &models.Table{Schema: "public", Name: "users"}
	b.TablePicker.sel = tbl
	var got []*models.Table
	b.HelperBag.OnTableActivate = func(tt *models.Table) error {
		got = append(got, tt)
		return nil
	}
	cur := &fakeCursor{}
	ctrl := controllers.NewTablesController(nil, b.HelperBag, cur, b.TablePicker)
	reg := commands.NewRegistry()
	ctrl.ListControllerTrait.RegisterActions(reg)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if isSpecial(kb, types.KeyEnter) {
			if err := invokeAction(reg, kb); err != nil {
				t.Fatalf("<CR>: %v", err)
			}
		}
	}
	if len(got) != 1 || got[0] != tbl {
		t.Fatalf("OnTableActivate calls = %+v, want 1 with users table", got)
	}
}

func TestTablesControllerEnterEmptyRailIsNoop(t *testing.T) {
	b := newBag()
	fired := 0
	b.HelperBag.OnTableActivate = func(*models.Table) error { fired++; return nil }
	cur := &fakeCursor{}
	ctrl := controllers.NewTablesController(nil, b.HelperBag, cur, b.TablePicker)
	reg := commands.NewRegistry()
	ctrl.ListControllerTrait.RegisterActions(reg)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if isSpecial(kb, types.KeyEnter) {
			_ = invokeAction(reg, kb)
		}
	}
	if fired != 0 {
		t.Fatalf("empty rail enter: OnTableActivate fired %d times, want 0", fired)
	}
}

// AC: <CR> with nil OnTableActivate is a clean no-op (no panic, no crash).
func TestTablesControllerEnterNilCallbackIsNoop(t *testing.T) {
	b := newBag()
	b.TablePicker.sel = &models.Table{Schema: "public", Name: "users"}
	// b.HelperBag.OnTableActivate intentionally left nil.
	cur := &fakeCursor{}
	ctrl := controllers.NewTablesController(nil, b.HelperBag, cur, b.TablePicker)
	reg := commands.NewRegistry()
	ctrl.ListControllerTrait.RegisterActions(reg)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if isSpecial(kb, types.KeyEnter) {
			if err := invokeAction(reg, kb); err != nil {
				t.Fatalf("<CR> with nil callback: %v", err)
			}
		}
	}
}
