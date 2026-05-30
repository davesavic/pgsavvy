package controllers

import (
	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// ConnectingController owns the CONNECTING screen's bindings: r invokes
// the injected Retry callback; <esc> invokes the injected Cancel
// callback. The callbacks are supplied at construction so this controller
// compiles and is unit-testable without the connect/cancel IO layer
// (dbsavvy-e53.3, T5 wires the real callbacks).
//
// Defaults are hardcoded (not user-overridable), mirroring
// ConfirmationController — the connecting screen must stay dismissable
// through the standard r / <esc> keys regardless of the user's config.
type ConnectingController struct {
	baseController

	// retry / cancel are the injected handlers. Either may be nil (test
	// fixtures / pre-T5 wiring); the handlers no-op rather than panic.
	retry  func()
	cancel func()
}

// NewConnectingController constructs the controller with injected Retry /
// Cancel callbacks. Both may be nil — the handlers no-op when unwired.
func NewConnectingController(c *common.Common, core CoreDeps, ui UIDeps, retry, cancel func()) *ConnectingController {
	return &ConnectingController{
		baseController: newBase(c, HelperBag{CoreDeps: core, UIDeps: ui}),
		retry:          retry,
		cancel:         cancel,
	}
}

// Retry dispatches into the injected retry callback. nil-safe when no
// callback is wired: returns nil.
func (cc *ConnectingController) Retry(_ commands.ExecCtx) error {
	if cc.retry == nil {
		return nil
	}
	cc.retry()
	return nil
}

// Cancel dispatches into the injected cancel callback. nil-safe when no
// callback is wired: returns nil.
func (cc *ConnectingController) Cancel(_ commands.ExecCtx) error {
	if cc.cancel == nil {
		return nil
	}
	cc.cancel()
	return nil
}

// GetKeybindings returns the CONNECTING-scope bindings. r → Retry;
// <esc> → Cancel.
func (cc *ConnectingController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := cc.tr()
	return []*types.ChordBinding{
		{
			Sequence:    []types.ChordKey{{Code: 'r'}},
			Mode:        types.ModeNormal,
			Scope:       types.CONNECTING,
			ActionID:    commands.ConnectingRetry,
			Description: tr.Actions.Reconnect,
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEsc}},
			Mode:        types.ModeNormal,
			Scope:       types.CONNECTING,
			ActionID:    commands.ConnectingCancel,
			Description: tr.Actions.Cancel,
		},
	}
}

// RegisterActions registers the retry / cancel handlers.
func (cc *ConnectingController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	_ = reg.Register(&commands.Command{
		ID:          commands.ConnectingRetry,
		Description: "Retry connection",
		Handler:     cc.Retry,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ConnectingCancel,
		Description: "Cancel connection",
		Handler:     cc.Cancel,
	})
}

// AttachToContext registers GetKeybindings on the CONNECTING context.
func (cc *ConnectingController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(cc.GetKeybindings)
}
