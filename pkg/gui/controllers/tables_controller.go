package controllers

import (
	"context"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// TablesController owns TABLES rail bindings: j/k via the trait, and
// <CR> fires HelperBag.OnTableActivate, which the orchestrator wires to
// a worker that loads columns for the selected table and pushes focus
// to the COLUMNS rail.
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
	confirm := func(_ commands.ExecCtx) error {
		if picker == nil || base.helpers.OnTableActivate == nil {
			return nil
		}
		t := picker.SelectedTable()
		if t == nil {
			return nil
		}
		err := base.helpers.OnTableActivate(t)
		return base.wrapErr("tables.confirm", err)
	}
	ctrl.ListControllerTrait = NewListControllerTrait(base, viewName(types.TABLES), cursor, picker, confirm)
	return ctrl
}

// RefreshRail is the `r` handler — reloads the TABLES rail for the
// currently-selected schema via HelperBag.Refresh. Nil-safe.
func (c *TablesController) RefreshRail(_ commands.ExecCtx) error {
	if c.helpers.Refresh == nil {
		return nil
	}
	schema := ""
	if c.helpers.Schemas != nil {
		schema = c.helpers.Schemas.SelectedSchemaName()
	}
	if schema == "" {
		return nil
	}
	return c.helpers.Refresh.RefreshTables(context.Background(), schema)
}

// GetKeybindings returns the tables rail bindings.
func (c *TablesController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := c.tr()
	view := viewName(types.TABLES)
	out := c.baseBindings()
	out = append(out, &types.ChordBinding{
		Sequence:    []types.ChordKey{{Code: 'r'}},
		Mode:        types.ModeNormal,
		Scope:       types.TABLES,
		ActionID:    listActionID(commands.RailRefresh, view),
		Description: tr.Actions.RefreshRail,
	})
	out = append(out, railSwitchBindings(view, tr)...)
	return out
}

// RegisterActions registers the `r` rail-refresh handler under the
// per-rail action ID.
func (c *TablesController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	_ = reg.Register(&commands.Command{
		ID:          listActionID(commands.RailRefresh, viewName(types.TABLES)),
		Description: "Refresh tables rail",
		Handler:     c.RefreshRail,
	})
}

// AttachToContext registers GetKeybindings.
func (c *TablesController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(c.GetKeybindings)
}
