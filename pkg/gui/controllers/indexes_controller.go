package controllers

import (
	"github.com/davesavic/dbsavvy/pkg/common"
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
	ctrl.ListControllerTrait = NewListControllerTrait[any](base, viewName(types.INDEXES), cursor, nil, func() error { return nil })
	return ctrl
}

// GetKeybindings returns the indexes rail bindings.
func (c *IndexesController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := c.tr()
	view := viewName(types.INDEXES)
	out := c.baseBindings()
	out = append(out, railSwitchBindings(view, tr)...)
	return out
}

// AttachToContext registers GetKeybindings.
func (c *IndexesController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(c.GetKeybindings)
}
