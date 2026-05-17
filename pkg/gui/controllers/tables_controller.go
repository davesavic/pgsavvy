package controllers

import (
	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// TablesController owns TABLES rail bindings: j/k via the trait, and
// <CR> emits the deferred-action toast through the TablesDoubleClickHelper.
type TablesController struct {
	*ListControllerTrait[TablePicker]
}

// NewTablesController constructs the controller.
func NewTablesController(
	c *common.Common,
	helpers HelperBag,
	cursor SideListCursor,
	picker TablePicker,
) *TablesController {
	base := newBase(c, helpers)
	ctrl := &TablesController{}
	confirm := func() error {
		if picker == nil || base.helpers.TableDouble == nil {
			return nil
		}
		t := picker.SelectedTable()
		if t == nil {
			return nil
		}
		err := base.helpers.TableDouble.DoubleClickStub(t)
		return base.wrapErr("tables.confirm", err)
	}
	ctrl.ListControllerTrait = NewListControllerTrait(base, viewName(types.TABLES), cursor, picker, confirm)
	return ctrl
}

// GetKeybindings returns the tables rail bindings.
func (c *TablesController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := c.tr()
	view := viewName(types.TABLES)
	out := c.baseBindings()
	out = append(out, railSwitchBindings(view, tr)...)
	return out
}

// AttachToContext registers GetKeybindings.
func (c *TablesController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(c.GetKeybindings)
}
