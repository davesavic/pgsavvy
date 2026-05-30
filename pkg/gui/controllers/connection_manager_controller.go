package controllers

import (
	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// ConnectionManagerController owns the CONNECTION_MANAGER modal's bindings
// (dbsavvy-ig4): <esc> invokes the injected Close callback. q is bound at
// CONNECTION_MANAGER scope to AppQuit (the modal is the startup root, so q
// quits). Ctrl-C quits via the GLOBAL-scope binding owned by QuitController.
//
// NOTE (dbsavvy-bsh): q is unconditionally bound to AppQuit here because the
// modal is the startup root in ig4's world. bsh will refine q to be
// context-aware (quit at stack root / close mid-session).
//
// The Close callback owns the root-exit semantics (epic-locked): when the
// modal is the startup root it is a no-op (Esc leaves the stack unchanged
// and never pops at stack bottom); when the modal was pushed over an active
// session it pops back. The orchestrator supplies the callback so this
// controller compiles and is unit-testable without the focus-stack handle,
// mirroring ConnectingController's injected cancel.
//
// Defaults are hardcoded (not user-overridable), mirroring
// ConnectingController.
type ConnectionManagerController struct {
	baseController

	// close is the injected Esc handler. May be nil (test fixtures /
	// pre-wiring); Close no-ops rather than panic.
	close func()
}

// NewConnectionManagerController constructs the controller with an injected
// Close callback. close may be nil — the handler no-ops when unwired.
func NewConnectionManagerController(c *common.Common, core CoreDeps, ui UIDeps, close func()) *ConnectionManagerController {
	return &ConnectionManagerController{
		baseController: newBase(c, HelperBag{CoreDeps: core, UIDeps: ui}),
		close:          close,
	}
}

// Close dispatches into the injected close callback. nil-safe when no
// callback is wired: returns nil.
func (cm *ConnectionManagerController) Close(_ commands.ExecCtx) error {
	if cm.close == nil {
		return nil
	}
	cm.close()
	return nil
}

// GetKeybindings returns the CONNECTION_MANAGER-scope bindings. <esc> →
// Close; q → AppQuit (modal is startup root; bsh will make q context-aware).
func (cm *ConnectionManagerController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := cm.tr()
	return []*types.ChordBinding{
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEsc}},
			Mode:        types.ModeNormal,
			Scope:       types.CONNECTION_MANAGER,
			ActionID:    commands.ConnectionManagerClose,
			Description: tr.Actions.Cancel,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'q'}},
			Mode:        types.ModeNormal,
			Scope:       types.CONNECTION_MANAGER,
			ActionID:    commands.AppQuit,
			Description: tr.Actions.QuitApp,
		},
	}
}

// RegisterActions registers the close handler.
func (cm *ConnectionManagerController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	_ = reg.Register(&commands.Command{
		ID:          commands.ConnectionManagerClose,
		Description: "Close connection manager",
		Handler:     cm.Close,
	})
}

// AttachToContext registers GetKeybindings on the CONNECTION_MANAGER context.
func (cm *ConnectionManagerController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(cm.GetKeybindings)
}
