package controllers

import (
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

// GetKeybindings returns the columns rail bindings.
func (c *ColumnsController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := c.tr()
	view := viewName(types.COLUMNS)
	out := c.baseBindings()
	out = append(out, railSwitchBindings(view, tr)...)
	return out
}

// RegisterActions is a no-op — columns has no rail-specific actions.
func (c *ColumnsController) RegisterActions(_ *commands.Registry) {}

// AttachToContext registers GetKeybindings.
func (c *ColumnsController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(c.GetKeybindings)
}
