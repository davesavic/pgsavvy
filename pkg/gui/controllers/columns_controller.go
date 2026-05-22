package controllers

import (
	"context"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// ColumnsController owns COLUMNS rail bindings (j/k via trait, plus
// rail-switch bindings). The COLUMNS rail has no row-activation
// affordance in this epic — <CR> is a no-op.
type ColumnsController struct {
	*ListControllerTrait[any]
}

// NewColumnsController constructs the controller.
func NewColumnsController(c *common.Common, helpers HelperBag, cursor SideListCursor) *ColumnsController {
	base := newBase(c, helpers)
	ctrl := &ColumnsController{}
	ctrl.ListControllerTrait = NewListControllerTrait[any](base, viewName(types.COLUMNS), cursor, nil, func(_ commands.ExecCtx) error { return nil })
	return ctrl
}

// RefreshRail is the `r` handler — reloads the COLUMNS rail for the
// currently-selected (schema, table) via HelperBag.Refresh. Nil-safe.
func (c *ColumnsController) RefreshRail(_ commands.ExecCtx) error {
	if c.helpers.Refresh == nil || c.helpers.Tables == nil {
		return nil
	}
	t := c.helpers.Tables.SelectedTable()
	if t == nil {
		return nil
	}
	return c.helpers.Refresh.RefreshColumns(context.Background(), t.Schema, t.Name)
}

// GetKeybindings returns the columns rail bindings.
func (c *ColumnsController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := c.tr()
	view := viewName(types.COLUMNS)
	out := c.baseBindings()
	out = append(out, &types.ChordBinding{
		Sequence:    []types.ChordKey{{Code: 'r'}},
		Mode:        types.ModeNormal,
		Scope:       types.COLUMNS,
		ActionID:    listActionID(commands.RailRefresh, view),
		Description: tr.Actions.RefreshRail,
	})
	out = append(out, railSwitchBindings(view, tr)...)
	return out
}

// RegisterActions registers the `r` rail-refresh handler under the
// per-rail action ID.
func (c *ColumnsController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	_ = reg.Register(&commands.Command{
		ID:          listActionID(commands.RailRefresh, viewName(types.COLUMNS)),
		Description: "Refresh columns rail",
		Handler:     c.RefreshRail,
	})
}

// AttachToContext registers GetKeybindings.
func (c *ColumnsController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(c.GetKeybindings)
}
