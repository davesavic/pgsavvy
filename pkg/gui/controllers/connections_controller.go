package controllers

import (
	"context"
	"strings"
	"time"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// connectErrToastTTL is the lifetime of the toast surfaced when
// Connect returns a non-fatal error (e.g. missing credentials, already
// connected). Long enough for the user to read the message, short
// enough that it disappears before they retry.
const connectErrToastTTL = 4 * time.Second

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
	confirm := func(_ commands.ExecCtx) error {
		if picker == nil || base.helpers.Connect == nil {
			return nil
		}
		profile := picker.SelectedConnection()
		if profile == nil {
			return nil
		}
		err := base.helpers.Connect.Connect(context.Background(), profile)
		if err == nil {
			return nil
		}
		// Log via wrapErr for debug-log breadcrumb, then surface to the
		// user as a sanitized toast and SWALLOW the error. Returning it
		// up to gocui crashes the MainLoop — for non-credential and
		// "already connected" errors the app must stay alive (bugs
		// dbsavvy-a07, dbsavvy-e9i).
		_ = base.wrapErr("connections.confirm", err)
		if base.helpers.Toast != nil {
			base.helpers.Toast.Show(config.SafeText(connectErrMessage(err)), connectErrToastTTL)
		}
		return nil
	}
	ctrl.ListControllerTrait = NewListControllerTrait(base, viewName(types.CONNECTIONS), cursor, picker, confirm)
	return ctrl
}

// AddConnection is the `a` handler. It invokes the WalkAddConnection
// flow through the helper interface; the real chained prompt lives in
// T7b's prompt helper and is plumbed into ConnectionFormHelper at the
// AttachControllers seam.
func (c *ConnectionsController) AddConnection(_ commands.ExecCtx) error {
	if c.helpers.ConnectionForm == nil {
		return nil
	}
	err := c.helpers.ConnectionForm.WalkAdd(context.Background())
	return c.wrapErr("connections.add", err)
}

// GetKeybindings returns the connections rail bindings.
func (c *ConnectionsController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := c.tr()
	view := viewName(types.CONNECTIONS)

	out := c.baseBindings()
	// `a` -> add connection.
	out = append(out, &types.ChordBinding{
		Sequence:    []types.ChordKey{{Code: 'a'}},
		Mode:        types.ModeNormal,
		Scope:       types.CONNECTIONS,
		ActionID:    commands.ConnectionAdd,
		Description: tr.Actions.AddConnection,
	})

	out = append(out, railSwitchBindings(view, tr)...)
	return out
}

// RegisterActions registers the rail-specific action handlers this
// controller owns with reg.
func (c *ConnectionsController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	_ = reg.Register(&commands.Command{
		ID:          commands.ConnectionAdd,
		Description: "Add connection",
		Handler:     c.AddConnection,
	})
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

// connectErrMessage returns the user-facing string for a Connect error.
// Strips known multi-line / "controller …:" wrapping that wrapErr adds,
// and rewrites the "already connected" sentinel into a friendlier
// short phrase. The returned string is passed through config.SafeText
// by the caller before reaching the toast surface.
func connectErrMessage(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	// data.ConnectHelper raises "data: already connected (call Disconnect first)"
	// when <cr> hits a profile that's already open. From the user's
	// perspective this is a no-op, not an error.
	if strings.Contains(msg, "already connected") {
		return "already connected"
	}
	return msg
}
