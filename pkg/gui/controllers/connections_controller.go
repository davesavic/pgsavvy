package controllers

import (
	"context"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// ConnectionsController owns keyboard bindings for the CONNECTIONS
// side rail. It composes ListControllerTrait for j/k navigation and
// <CR> activation; rail-specific bindings (a → add, digits → switch,
// tab → cycle) live alongside.
type ConnectionsController struct {
	*ListControllerTrait[ConnectionPicker]
}

// NewConnectionsController constructs the controller. Pass the
// CONNECTIONS context's SideListCursor and the same picker
// (typically the same context, since ConnectionsContext is both).
func NewConnectionsController(
	c *common.Common,
	helpers HelperBag,
	cursor SideListCursor,
	picker ConnectionPicker,
) *ConnectionsController {
	base := newBase(c, helpers)
	ctrl := &ConnectionsController{}
	confirm := func() error {
		if picker == nil || base.helpers.Connect == nil {
			return nil
		}
		profile := picker.SelectedConnection()
		if profile == nil {
			return nil
		}
		err := base.helpers.Connect.Connect(context.Background(), profile)
		return base.wrapErr("connections.confirm", err)
	}
	ctrl.ListControllerTrait = NewListControllerTrait(base, viewName(types.CONNECTIONS), cursor, picker, confirm)
	return ctrl
}

// AddConnection is the `a` handler. It invokes the WalkAddConnection
// flow through the helper interface; the real chained prompt lives in
// T7b's prompt helper and is plumbed into ConnectionFormHelper at the
// AttachControllers seam.
func (c *ConnectionsController) AddConnection() error {
	if c.helpers.ConnectionForm == nil {
		return nil
	}
	err := c.helpers.ConnectionForm.WalkAdd(context.Background())
	return c.wrapErr("connections.add", err)
}

// GetKeybindings returns the connections rail bindings.
func (c *ConnectionsController) GetKeybindings(_ types.KeybindingsOpts) []*types.KeyBinding {
	tr := c.tr()
	view := viewName(types.CONNECTIONS)

	out := c.baseBindings()
	// `a` -> add connection (M11c).
	out = append(out, &types.KeyBinding{
		ViewName:    view,
		Key:         gocui.NewKeyRune('a'),
		Mod:         gocui.ModNone,
		Handler:     c.AddConnection,
		Description: tr.Actions.AddConnection,
	})

	// Rail switches (digit + <tab>) are appended by AttachControllers via
	// a helper because every side controller shares the same set; keeping
	// the digit bindings in one place avoids drift.
	out = append(out, railSwitchBindings(view, tr)...)
	return out
}

// AttachToContext registers GetKeybindings with the supplied context so
// the runtime collects this controller's bindings during the registration
// pass.
func (c *ConnectionsController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(c.GetKeybindings)
}
