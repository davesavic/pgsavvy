package controllers

import (
	"context"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// IndexesController owns INDEXES rail bindings (j/k via trait, plus
// rail-switch bindings).
type IndexesController struct {
	*ListControllerTrait[any]
}

// NewIndexesController constructs the controller.
func NewIndexesController(c *common.Common, helpers HelperBag, cursor SideListCursor) *IndexesController {
	base := newBase(c, helpers)
	ctrl := &IndexesController{}
	ctrl.ListControllerTrait = NewListControllerTrait[any](base, viewName(types.INDEXES), cursor, nil, func(_ commands.ExecCtx) error { return nil })
	return ctrl
}

// RefreshRail is the `r` handler — reloads the INDEXES rail for the
// currently-selected (schema, table) via HelperBag.Refresh. Nil-safe.
func (c *IndexesController) RefreshRail(_ commands.ExecCtx) error {
	if c.helpers.Refresh == nil || c.helpers.Tables == nil {
		return nil
	}
	t := c.helpers.Tables.SelectedTable()
	if t == nil {
		return nil
	}
	return c.helpers.Refresh.RefreshIndexes(context.Background(), t.Schema, t.Name)
}

// GetKeybindings returns the indexes rail bindings.
func (c *IndexesController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := c.tr()
	view := viewName(types.INDEXES)
	out := c.baseBindings()
	out = append(out, &types.ChordBinding{
		Sequence:    []types.ChordKey{{Code: 'r'}},
		Mode:        types.ModeNormal,
		Scope:       types.INDEXES,
		ActionID:    listActionID(commands.RailRefresh, view),
		Description: tr.Actions.RefreshRail,
	})
	out = append(out, railSwitchBindings(view, tr)...)
	return out
}

// RegisterActions registers the `r` rail-refresh handler under the
// per-rail action ID.
func (c *IndexesController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	_ = reg.Register(&commands.Command{
		ID:          listActionID(commands.RailRefresh, viewName(types.INDEXES)),
		Description: "Refresh indexes rail",
		Handler:     c.RefreshRail,
	})
}

// AttachToContext registers GetKeybindings.
func (c *IndexesController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(c.GetKeybindings)
}
