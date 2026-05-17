package controllers_test

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// AC: tables_controller `<CR>` emits the deferred-action toast via the
// TablesDoubleClickHelper interface.
func TestTablesControllerConfirmCallsDoubleClickStub(t *testing.T) {
	b := newBag()
	tbl := &models.Table{Name: "users"}
	b.TablePicker.sel = tbl
	cur := &fakeCursor{}
	ctrl := controllers.NewTablesController(nil, b.HelperBag, cur, b.TablePicker)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if isSpecial(kb, types.KeyEnter) {
			if err := kb.Handler(); err != nil {
				t.Fatalf("<CR>: %v", err)
			}
		}
	}
	if len(b.TableDouble.calls) != 1 || b.TableDouble.calls[0] != tbl {
		t.Fatalf("DoubleClickStub calls = %v, want 1 with users table", b.TableDouble.calls)
	}
}

func TestTablesControllerEnterEmptyRailIsNoop(t *testing.T) {
	b := newBag()
	cur := &fakeCursor{}
	ctrl := controllers.NewTablesController(nil, b.HelperBag, cur, b.TablePicker)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if isSpecial(kb, types.KeyEnter) {
			_ = kb.Handler()
		}
	}
	if len(b.TableDouble.calls) != 0 {
		t.Fatalf("empty rail enter: DoubleClickStub called %d times, want 0", len(b.TableDouble.calls))
	}
}
