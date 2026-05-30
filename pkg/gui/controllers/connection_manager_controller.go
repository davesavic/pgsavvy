package controllers

import (
	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// ConnectionManagerController owns the CONNECTION_MANAGER modal's bindings
// (dbsavvy-ig4 scaffold, dbsavvy-1rf list + in-modal connect):
//
//   - list mode: j/k move the cursor, <CR> connects the selected profile
//     (switches the modal into connecting mode), <esc> closes the modal.
//   - connecting mode: <CR>/r re-attempt (Retry), <esc> cancels the in-flight
//     dial and returns the modal to list mode (does NOT close it).
//
// q is bound at CONNECTION_MANAGER scope to AppQuit (the modal is the startup
// root, so q quits). Ctrl-C quits via the GLOBAL-scope binding owned by
// QuitController.
//
// The injected callbacks (close + the ConnectionManagerDeps closures) keep
// this controller compilable and unit-testable without the focus-stack /
// connect-IO handles, mirroring ConnectingController's injected cancel/retry.
// Defaults are hardcoded (not user-overridable).
type ConnectionManagerController struct {
	baseController

	// close is the injected Esc-in-list-mode handler. May be nil (test
	// fixtures / pre-wiring); the handler no-ops rather than panic.
	close func()

	// deps carries the list/connect closures. Optional: an unset deps (the
	// 4-arg constructor) leaves the modal a close-only screen, matching the
	// ig4 scaffold contract.
	deps ConnectionManagerDeps
}

// ConnectionManagerDeps bundles the list + in-modal-connect closures the
// orchestrator wires (dbsavvy-1rf). Every field is optional; nil fields make
// their handler a no-op.
type ConnectionManagerDeps struct {
	// Ctx is the live modal context — the controller reads its mode + cursor
	// + selected row, and flips it into connecting mode on <CR>.
	Ctx *context.ConnectionManagerContext
	// Connect starts the in-modal connect lifecycle for the profile (wired to
	// connectInvoker's modal-origin attempt).
	Connect func(*models.Connection)
	// Retry re-attempts the most recent modal profile from the error state.
	Retry func()
	// CancelConnecting aborts the in-flight dial and returns the modal to list
	// mode.
	CancelConnecting func()
}

// NewConnectionManagerController constructs the controller with an injected
// Close callback. close may be nil — the handler no-ops when unwired. The
// list/connect closures are wired separately via SetDeps so the scaffold's
// 4-arg signature stays intact (dbsavvy-1rf).
func NewConnectionManagerController(c *common.Common, core CoreDeps, ui UIDeps, close func()) *ConnectionManagerController {
	return &ConnectionManagerController{
		baseController: newBase(c, HelperBag{CoreDeps: core, UIDeps: ui}),
		close:          close,
	}
}

// SetDeps wires the list + in-modal-connect closures (dbsavvy-1rf). Called by
// the orchestrator once the modal context + connectInvoker exist.
func (cm *ConnectionManagerController) SetDeps(d ConnectionManagerDeps) { cm.deps = d }

// inConnectingMode reports whether the modal is currently rendering the
// connecting/error body. False when the context is unwired (scaffold path).
func (cm *ConnectionManagerController) inConnectingMode() bool {
	return cm.deps.Ctx != nil && cm.deps.Ctx.Mode() == context.ModeConnecting
}

// Close handles <esc>. In connecting mode it cancels the in-flight dial and
// returns to list mode; in list mode it dispatches the injected close
// callback. nil-safe throughout.
func (cm *ConnectionManagerController) Close(_ commands.ExecCtx) error {
	if cm.inConnectingMode() {
		if cm.deps.CancelConnecting != nil {
			cm.deps.CancelConnecting()
		}
		return nil
	}
	if cm.close == nil {
		return nil
	}
	cm.close()
	return nil
}

// Down moves the list cursor down. No-op in connecting mode or when unwired.
func (cm *ConnectionManagerController) Down(_ commands.ExecCtx) error {
	if cm.deps.Ctx == nil || cm.inConnectingMode() {
		return nil
	}
	cm.deps.Ctx.SetCursor(cm.deps.Ctx.Cursor() + 1)
	return nil
}

// Up moves the list cursor up. No-op in connecting mode or when unwired.
func (cm *ConnectionManagerController) Up(_ commands.ExecCtx) error {
	if cm.deps.Ctx == nil || cm.inConnectingMode() {
		return nil
	}
	cm.deps.Ctx.SetCursor(cm.deps.Ctx.Cursor() - 1)
	return nil
}

// Confirm handles <CR>. In connecting mode it re-attempts (Retry); in list
// mode it flips the modal into connecting mode and starts the connect
// lifecycle for the selected profile. nil-safe throughout.
func (cm *ConnectionManagerController) Confirm(_ commands.ExecCtx) error {
	if cm.inConnectingMode() {
		if cm.deps.Retry != nil {
			cm.deps.Retry()
		}
		return nil
	}
	if cm.deps.Ctx == nil || cm.deps.Connect == nil {
		return nil
	}
	conn, ok := cm.deps.Ctx.SelectedItem().(*models.Connection)
	if !ok || conn == nil {
		return nil
	}
	cm.deps.Connect(conn)
	return nil
}

// Retry handles r in connecting mode → re-attempt the most recent profile.
// No-op in list mode or when unwired.
func (cm *ConnectionManagerController) Retry(_ commands.ExecCtx) error {
	if !cm.inConnectingMode() || cm.deps.Retry == nil {
		return nil
	}
	cm.deps.Retry()
	return nil
}

// GetKeybindings returns the CONNECTION_MANAGER-scope bindings. j/k → cursor;
// <CR> → Confirm (connect / retry); r → Retry (connecting mode); <esc> →
// Close (close / cancel); q → AppQuit.
func (cm *ConnectionManagerController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := cm.tr()
	return []*types.ChordBinding{
		{
			Sequence:    []types.ChordKey{{Code: 'j'}},
			Mode:        types.ModeNormal,
			Scope:       types.CONNECTION_MANAGER,
			ActionID:    commands.ConnectionManagerDown,
			Description: tr.Actions.Down,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'k'}},
			Mode:        types.ModeNormal,
			Scope:       types.CONNECTION_MANAGER,
			ActionID:    commands.ConnectionManagerUp,
			Description: tr.Actions.Up,
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEnter}},
			Mode:        types.ModeNormal,
			Scope:       types.CONNECTION_MANAGER,
			ActionID:    commands.ConnectionManagerConfirm,
			Description: tr.Actions.Confirm,
			ShowInBar:   true,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'r'}},
			Mode:        types.ModeNormal,
			Scope:       types.CONNECTION_MANAGER,
			ActionID:    commands.ConnectionManagerRetry,
			Description: tr.Actions.Reconnect,
		},
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

// RegisterActions registers the modal's handlers.
func (cm *ConnectionManagerController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	_ = reg.Register(&commands.Command{
		ID:          commands.ConnectionManagerClose,
		Description: "Close connection manager",
		Handler:     cm.Close,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ConnectionManagerDown,
		Description: "Move connection manager cursor down",
		Handler:     cm.Down,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ConnectionManagerUp,
		Description: "Move connection manager cursor up",
		Handler:     cm.Up,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ConnectionManagerConfirm,
		Description: "Connect / retry from connection manager",
		Handler:     cm.Confirm,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ConnectionManagerRetry,
		Description: "Retry connection from connection manager",
		Handler:     cm.Retry,
	})
}

// AttachToContext registers GetKeybindings on the CONNECTION_MANAGER context.
func (cm *ConnectionManagerController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(cm.GetKeybindings)
}
