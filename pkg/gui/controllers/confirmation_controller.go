package controllers

import (
	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// ConfirmationController owns the CONFIRMATION popup bindings: y / <cr>
// invoke ConfirmHelper.Yes, n / <esc> invoke ConfirmHelper.No. The helper
// owns the popup's pending onYes / onNo callbacks and the focus-stack
// pop on dismissal; this controller is a thin dispatcher.
//
// Defaults are hardcoded (not user-overridable) per AD-4 —
// confirmation popups must be dismissable through the standard
// y/n/<cr>/<esc> keys regardless of the user's config.
type ConfirmationController struct {
	baseController
}

// NewConfirmationController constructs the controller.
func NewConfirmationController(c *common.Common, core CoreDeps, ui UIDeps) *ConfirmationController {
	return &ConfirmationController{baseController: newBase(c, HelperBag{CoreDeps: core, UIDeps: ui})}
}

// Yes dispatches into ConfirmHelper.Yes. nil-safe when no Confirm helper
// is wired (test fixtures): returns nil.
func (cc *ConfirmationController) Yes(_ commands.ExecCtx) error {
	if cc.helpers.Confirm == nil {
		return nil
	}
	return cc.helpers.Confirm.Yes()
}

// No dispatches into ConfirmHelper.No. nil-safe when no Confirm helper
// is wired.
func (cc *ConfirmationController) No(_ commands.ExecCtx) error {
	if cc.helpers.Confirm == nil {
		return nil
	}
	return cc.helpers.Confirm.No()
}

// GetKeybindings returns the CONFIRMATION-scope bindings. y/<cr> → Yes;
// n/<esc> → No.
func (cc *ConfirmationController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := cc.tr()
	return []*types.ChordBinding{
		{
			Sequence:    []types.ChordKey{{Code: 'y'}},
			Mode:        types.ModeNormal,
			Scope:       types.CONFIRMATION,
			ActionID:    commands.ConfirmYes,
			Description: tr.Actions.Confirm,
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEnter}},
			Mode:        types.ModeNormal,
			Scope:       types.CONFIRMATION,
			ActionID:    commands.ConfirmYes,
			Description: tr.Actions.Confirm,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'n'}},
			Mode:        types.ModeNormal,
			Scope:       types.CONFIRMATION,
			ActionID:    commands.ConfirmNo,
			Description: tr.Actions.Cancel,
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEsc}},
			Mode:        types.ModeNormal,
			Scope:       types.CONFIRMATION,
			ActionID:    commands.ConfirmNo,
			Description: tr.Actions.Cancel,
		},
	}
}

// RegisterActions registers the yes / no handlers.
func (cc *ConfirmationController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	_ = reg.Register(&commands.Command{
		ID:          commands.ConfirmYes,
		Description: "Confirm yes",
		Handler:     cc.Yes,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ConfirmNo,
		Description: "Confirm no",
		Handler:     cc.No,
	})
}

// AttachToContext registers GetKeybindings on the CONFIRMATION context.
func (cc *ConfirmationController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(cc.GetKeybindings)
}
