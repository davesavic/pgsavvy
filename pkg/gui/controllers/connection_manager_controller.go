package controllers

import (
	"fmt"
	"strings"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// ConnectionManagerController owns the CONNECTION_MANAGER modal's bindings
// (scaffold + list + in-modal connect):
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
	// scaffold contract.
	deps ConnectionManagerDeps
}

// ConnectionManagerDeps bundles the list + in-modal-connect closures the
// orchestrator wires. Every field is optional; nil fields make
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

	// Prompt pushes the single-line PROMPT popup for editing a text field.
	// It is the same helper the connection add-flow drives;
	// the popup stacks ON TOP of the modal and returns control to ModeForm on
	// close. nil leaves text-field editing a no-op.
	Prompt PromptHelper

	// ExistingNames snapshots all profile names for the form's
	// uniqueness check. nil → empty snapshot (no duplicates flagged).
	ExistingNames func() []string

	// DriversFn supplies the driver-selector list. nil → drivers.Names.
	DriversFn func() []string

	// OnSaveConnection is the save seam zod populates (a
	// no-op stub here — no config write). It is invoked with the validated
	// connection after Enter passes validate-all. isEdit + originalName let
	// the writer distinguish append vs update + handle renames. A nil
	// callback (or a non-nil one returning nil) returns the form to ModeList.
	OnSaveConnection func(conn models.Connection, isEdit bool, originalName string) error

	// OnDeleteConnection is the delete seam. Invoked with the
	// connection name after the user confirms deletion. The orchestrator
	// callback tears down the active session if needed, calls
	// config.DeleteConnection, and refreshes the modal list. A nil callback
	// makes Delete a no-op.
	OnDeleteConnection func(connName string) error

	// ReadClipboard returns the host clipboard contents for the paste-DSN
	// action. nil → paste is a no-op.
	ReadClipboard func() (string, error)

	// TestConnection dials the in-progress (unsaved) connection from the form
	// and publishes the pass/fail result INLINE in the form. The closure owns
	// threading (worker dial + UI-thread publish) and the stale-write guard;
	// it does NOT return synchronously, mirroring the async Connect closure.
	// nil → the test action is a no-op.
	TestConnection func(*models.Connection)

	// ShowToast surfaces a transient notice (e.g. the dropped-password warning
	// after a paste). nil → no toast.
	ShowToast func(string)

	// StackDepth returns the current focus-stack depth. Used by
	// QuitOrClose to distinguish startup root (depth 1 → quit) from
	// mid-session open (depth > 1 → close modal). Nil-safe: defaults to
	// 1 (quit).
	StackDepth func() int
}

// QuitOrClose handles q on the CONNECTION_MANAGER modal. At startup root
// (stack depth == 1) it quits the app; mid-session (stack depth > 1) it
// closes the modal back to data.
func (cm *ConnectionManagerController) QuitOrClose(ec commands.ExecCtx) error {
	depth := 1
	if cm.deps.StackDepth != nil {
		depth = cm.deps.StackDepth()
	}
	if depth <= 1 {
		// Startup root: dispatch AppQuit.
		return gocui.ErrQuit
	}
	// Mid-session: close the modal.
	return cm.Close(ec)
}

// NewConnectionManagerController constructs the controller with an injected
// Close callback. close may be nil — the handler no-ops when unwired. The
// list/connect closures are wired separately via SetDeps so the scaffold's
// 4-arg signature stays intact.
func NewConnectionManagerController(c *common.Common, core CoreDeps, ui UIDeps, close func()) *ConnectionManagerController {
	return &ConnectionManagerController{
		baseController: newBase(c, HelperBag{CoreDeps: core, UIDeps: ui}),
		close:          close,
	}
}

// SetDeps wires the list + in-modal-connect closures. Called by
// the orchestrator once the modal context + connectInvoker exist.
func (cm *ConnectionManagerController) SetDeps(d ConnectionManagerDeps) { cm.deps = d }

// inConnectingMode reports whether the modal is currently rendering the
// connecting/error body. False when the context is unwired (scaffold path).
func (cm *ConnectionManagerController) inConnectingMode() bool {
	return cm.deps.Ctx != nil && cm.deps.Ctx.Mode() == context.ModeConnecting
}

// inErrorState reports whether the connecting modal is in its error sub-phase
// (a failed attempt awaiting retry/back). Retry is gated on this: during the
// active-dial phase (connecting body, no error) only Esc is allowed, since
// retrying mid-dial supersedes the in-flight attempt and re-prompts for
// credentials.
func (cm *ConnectionManagerController) inErrorState() bool {
	return cm.inConnectingMode() && cm.deps.Ctx.ConnectingState().IsError()
}

// inFormMode reports whether the modal is rendering the add/edit form. False
// when the context is unwired.
func (cm *ConnectionManagerController) inFormMode() bool {
	return cm.deps.Ctx != nil && cm.deps.Ctx.Mode() == context.ModeForm
}

// existingNames returns the profile-name snapshot for the form's uniqueness
// check. Empty when unwired.
func (cm *ConnectionManagerController) existingNames() []string {
	if cm.deps.ExistingNames == nil {
		return nil
	}
	return cm.deps.ExistingNames()
}

// Close handles <esc>. In connecting mode it cancels the in-flight dial and
// returns to list mode; in form mode it cancels the form back to the list; in
// list mode it dispatches the injected close callback. nil-safe throughout.
func (cm *ConnectionManagerController) Close(_ commands.ExecCtx) error {
	if cm.inConnectingMode() {
		if cm.deps.CancelConnecting != nil {
			cm.deps.CancelConnecting()
		}
		return nil
	}
	if cm.inFormMode() {
		cm.deps.Ctx.SetMode(context.ModeList)
		return nil
	}
	if cm.close == nil {
		return nil
	}
	cm.close()
	return nil
}

// Down moves the list cursor down, or — in form mode — the field focus down.
// No-op in connecting mode or when unwired.
func (cm *ConnectionManagerController) Down(_ commands.ExecCtx) error {
	if cm.deps.Ctx == nil || cm.inConnectingMode() {
		return nil
	}
	if cm.inFormMode() {
		cm.deps.Ctx.FormMoveFocus(1)
		return nil
	}
	cm.deps.Ctx.SetCursor(cm.deps.Ctx.Cursor() + 1)
	return nil
}

// Up moves the list cursor up, or — in form mode — the field focus up. No-op
// in connecting mode or when unwired.
func (cm *ConnectionManagerController) Up(_ commands.ExecCtx) error {
	if cm.deps.Ctx == nil || cm.inConnectingMode() {
		return nil
	}
	if cm.inFormMode() {
		cm.deps.Ctx.FormMoveFocus(-1)
		return nil
	}
	cm.deps.Ctx.SetCursor(cm.deps.Ctx.Cursor() - 1)
	return nil
}

// First jumps the list cursor to the first profile. No-op in connecting or
// form mode (where gg/G carry no list meaning) or when unwired.
func (cm *ConnectionManagerController) First(_ commands.ExecCtx) error {
	if cm.deps.Ctx == nil || cm.inConnectingMode() || cm.inFormMode() {
		return nil
	}
	cm.deps.Ctx.SetCursor(0)
	return nil
}

// Last jumps the list cursor to the final profile. No-op in connecting or
// form mode, on an empty list, or when unwired.
func (cm *ConnectionManagerController) Last(_ commands.ExecCtx) error {
	if cm.deps.Ctx == nil || cm.inConnectingMode() || cm.inFormMode() {
		return nil
	}
	n := len(cm.deps.Ctx.Items())
	if n == 0 {
		return nil
	}
	cm.deps.Ctx.SetCursor(n - 1)
	return nil
}

// Confirm handles <CR>. In connecting mode it re-attempts (Retry); in form
// mode it saves the form (validate-all → OnSaveConnection → ModeList); in list
// mode it flips the modal into connecting mode and starts the connect
// lifecycle for the selected profile. nil-safe throughout.
func (cm *ConnectionManagerController) Confirm(_ commands.ExecCtx) error {
	if cm.inConnectingMode() {
		// Active dial: swallow Enter (only Esc cancels). Retry only from the
		// error sub-phase — see inErrorState.
		if cm.inErrorState() && cm.deps.Retry != nil {
			cm.deps.Retry()
		}
		return nil
	}
	if cm.inFormMode() {
		return cm.saveForm()
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

// saveForm runs validate-all and, on success, invokes the injected save
// callback and returns the modal to list mode. On validation failure it stays
// in the form with the inline error rendered (the context stamps the error +
// moves focus onto the offending field).
func (cm *ConnectionManagerController) saveForm() error {
	conn, isEdit, originalName, ok := cm.deps.Ctx.FormValidateAll(cm.tr())
	if !ok {
		return nil
	}
	if cm.deps.OnSaveConnection != nil {
		if err := cm.deps.OnSaveConnection(conn, isEdit, originalName); err != nil {
			return err
		}
	}
	cm.deps.Ctx.SetMode(context.ModeList)
	return nil
}

// Add opens a blank add form (ModeList only). Wires the empty-state "[a] add"
// hint. No-op when unwired or already in form/connecting mode.
func (cm *ConnectionManagerController) Add(_ commands.ExecCtx) error {
	if cm.deps.Ctx == nil || cm.inConnectingMode() || cm.inFormMode() {
		return nil
	}
	cm.deps.Ctx.OpenAddForm(cm.existingNames(), cm.deps.DriversFn)
	return nil
}

// Edit is the `i` handler. In form mode it routes to FieldEdit so the same key
// edits the focused field; otherwise it opens the form seeded from the selected
// list row. No-op on an empty list or when unwired.
func (cm *ConnectionManagerController) Edit(ec commands.ExecCtx) error {
	if cm.inFormMode() {
		return cm.FieldEdit(ec)
	}
	if cm.deps.Ctx == nil || cm.inConnectingMode() {
		return nil
	}
	conn, ok := cm.deps.Ctx.SelectedItem().(*models.Connection)
	if !ok || conn == nil {
		return nil
	}
	cm.deps.Ctx.OpenEditForm(*conn, cm.existingNames(), cm.deps.DriversFn)
	return nil
}

// Delete opens a confirmation prompt for the selected connection (ModeList
// only). On confirm: invokes OnDeleteConnection. No-op when unwired, in
// form/connecting mode, or on an empty list.
func (cm *ConnectionManagerController) Delete(_ commands.ExecCtx) error {
	if cm.deps.Ctx == nil || cm.inConnectingMode() || cm.inFormMode() {
		return nil
	}
	conn, ok := cm.deps.Ctx.SelectedItem().(*models.Connection)
	if !ok || conn == nil {
		return nil
	}
	if cm.helpers.Confirm == nil || cm.deps.OnDeleteConnection == nil {
		return nil
	}
	tr := cm.tr()
	name := conn.Name
	return cm.helpers.Confirm.Confirm(
		tr.AreYouSure,
		fmt.Sprintf("Delete \"%s\"?", name),
		func() error { return cm.deps.OnDeleteConnection(name) },
		nil,
	)
}

// FieldNext moves field focus forward (Tab). Form mode only.
func (cm *ConnectionManagerController) FieldNext(_ commands.ExecCtx) error {
	if !cm.inFormMode() {
		return nil
	}
	cm.deps.Ctx.FormMoveFocus(1)
	return nil
}

// FieldPrev moves field focus backward (Shift-Tab). Form mode only.
func (cm *ConnectionManagerController) FieldPrev(_ commands.ExecCtx) error {
	if !cm.inFormMode() {
		return nil
	}
	cm.deps.Ctx.FormMoveFocus(-1)
	return nil
}

// FieldEdit handles i. On a text field it opens the PROMPT popup seeded with
// the current value + that field's validator; on submit the validated value
// is stored back into the form. On a toggle/driver field it flips/cycles in
// place. Form mode only.
func (cm *ConnectionManagerController) FieldEdit(_ commands.ExecCtx) error {
	if !cm.inFormMode() {
		return nil
	}
	if !cm.deps.Ctx.FormFocusedIsText() {
		cm.deps.Ctx.FormToggleFocused()
		return nil
	}
	if cm.deps.Prompt == nil {
		return nil
	}
	ctx := cm.deps.Ctx
	label := ctx.FormFocusedLabel()
	initial := ctx.FormFocusedValue()
	validate := ctx.FormFocusedValidator(cm.tr())
	return cm.deps.Prompt.Prompt(label, initial,
		func(value string) error {
			// The PROMPT popup pops on submit before this runs, so a
			// validation failure renders inline in the form (the user
			// re-opens the field to retry) rather than re-prompting.
			if validate != nil {
				if err := validate(value); err != nil {
					ctx.FormSetError(err.Error())
					return nil
				}
			}
			ctx.FormSetFocusedValue(value)
			return nil
		},
		func() error { return nil },
	)
}

// Toggle handles space → flips the focused toggle / cycles the driver. A
// no-op on text fields. Form mode only.
func (cm *ConnectionManagerController) Toggle(_ commands.ExecCtx) error {
	if !cm.inFormMode() {
		return nil
	}
	if cm.deps.Ctx.FormFocusedIsText() {
		return nil
	}
	cm.deps.Ctx.FormToggleFocused()
	return nil
}

// PasteDSN reads a DSN from the clipboard and populates the form's discrete
// fields (form mode only). A pasted inline password is dropped — never stored —
// and a toast warns the user. Unparseable input stamps an inline error and
// leaves the fields untouched.
func (cm *ConnectionManagerController) PasteDSN(_ commands.ExecCtx) error {
	if !cm.inFormMode() || cm.deps.Ctx == nil || cm.deps.ReadClipboard == nil {
		return nil
	}
	raw, err := cm.deps.ReadClipboard()
	if err != nil || strings.TrimSpace(raw) == "" {
		cm.deps.Ctx.FormSetError("clipboard is empty")
		return nil
	}
	hadPassword, ok := cm.deps.Ctx.FormApplyDSN(strings.TrimSpace(raw))
	if ok && hadPassword && cm.deps.ShowToast != nil {
		cm.deps.ShowToast("Password not stored — you'll be prompted on connect")
	}
	return nil
}

// TestConnection handles `t` in form mode: it dials the in-progress (unsaved)
// connection being edited and reports pass/fail INLINE in the form, without
// establishing the real session, touching the live active connection, or
// popping the modal. The injected closure owns threading + the inline publish;
// this handler only gates on form mode and forwards a copy of the in-progress
// connection. No-op outside form mode or when unwired.
func (cm *ConnectionManagerController) TestConnection(_ commands.ExecCtx) error {
	if !cm.inFormMode() || cm.deps.Ctx == nil || cm.deps.TestConnection == nil {
		return nil
	}
	conn := cm.deps.Ctx.FormConnection()
	cm.deps.TestConnection(&conn)
	return nil
}

// Retry handles r → re-attempt the most recent profile. Only fires from the
// error sub-phase; inert during the active dial (where only Esc cancels) and
// in list mode or when unwired.
func (cm *ConnectionManagerController) Retry(_ commands.ExecCtx) error {
	if !cm.inErrorState() || cm.deps.Retry == nil {
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
			Sequence:    []types.ChordKey{{Code: 'g'}, {Code: 'g'}},
			Mode:        types.ModeNormal,
			Scope:       types.CONNECTION_MANAGER,
			ActionID:    commands.ConnectionManagerJumpFirst,
			Description: tr.Actions.JumpFirst,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'G'}},
			Mode:        types.ModeNormal,
			Scope:       types.CONNECTION_MANAGER,
			ActionID:    commands.ConnectionManagerJumpLast,
			Description: tr.Actions.JumpLast,
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
			ShowInBar:   true,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'a'}},
			Mode:        types.ModeNormal,
			Scope:       types.CONNECTION_MANAGER,
			ActionID:    commands.ConnectionManagerAdd,
			Description: tr.Actions.AddConnection,
			ShowInBar:   true,
		},
		{
			// `i` is the single context-sensitive edit key: in list mode it
			// opens the edit-connection form; in form mode the Edit handler
			// routes to FieldEdit so the same key edits the focused field. The
			// trie is last-wins, so the two behaviours share ONE binding.
			Sequence:    []types.ChordKey{{Code: 'i'}},
			Mode:        types.ModeNormal,
			Scope:       types.CONNECTION_MANAGER,
			ActionID:    commands.ConnectionManagerEdit,
			Description: tr.Actions.EditConnection,
			ShowInBar:   true,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'd'}},
			Mode:        types.ModeNormal,
			Scope:       types.CONNECTION_MANAGER,
			ActionID:    commands.ConnectionManagerDelete,
			Description: tr.Actions.DeleteConnection,
			ShowInBar:   true,
		},
		{
			Sequence:    []types.ChordKey{{Code: ' '}},
			Mode:        types.ModeNormal,
			Scope:       types.CONNECTION_MANAGER,
			ActionID:    commands.ConnectionManagerToggle,
			Description: tr.Actions.ToggleField,
		},
		{
			// <leader>p — paste a DSN into the discrete fields. Kept off the
			// options bar (no ShowInBar) so it doesn't crowd the 8-entry cap.
			Sequence:    []types.ChordKey{{Special: types.KeyLeader}, {Code: 'p'}},
			Mode:        types.ModeNormal,
			Scope:       types.CONNECTION_MANAGER,
			ActionID:    commands.ConnectionManagerPasteDSN,
			Description: tr.Actions.PasteDSN,
		},
		{
			// `t` — test the in-progress connection in form mode. Kept off the
			// options bar (no ShowInBar) so it doesn't crowd the cap; the
			// handler is mode-gated to form mode (no-op elsewhere).
			Sequence:    []types.ChordKey{{Code: 't'}},
			Mode:        types.ModeNormal,
			Scope:       types.CONNECTION_MANAGER,
			ActionID:    commands.ConnectionManagerTestConnection,
			Description: tr.Actions.TestConnection,
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyTab}},
			Mode:        types.ModeNormal,
			Scope:       types.CONNECTION_MANAGER,
			ActionID:    commands.ConnectionManagerFieldNext,
			Description: tr.Actions.NextField,
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyTab, Mod: types.ChordModShift}},
			Mode:        types.ModeNormal,
			Scope:       types.CONNECTION_MANAGER,
			ActionID:    commands.ConnectionManagerFieldPrev,
			Description: tr.Actions.PrevField,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'q'}},
			Mode:        types.ModeNormal,
			Scope:       types.CONNECTION_MANAGER,
			ActionID:    commands.ConnectionManagerQuitOrClose,
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
		ID:          commands.ConnectionManagerQuitOrClose,
		Description: "Quit or close connection manager",
		Handler:     cm.QuitOrClose,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ConnectionManagerClose,
		Description: "Close",
		Handler:     cm.Close,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ConnectionManagerDown,
		Description: "Cursor down",
		Handler:     cm.Down,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ConnectionManagerUp,
		Description: "Cursor up",
		Handler:     cm.Up,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ConnectionManagerJumpFirst,
		Description: "Jump to first row",
		Handler:     cm.First,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ConnectionManagerJumpLast,
		Description: "Jump to last row",
		Handler:     cm.Last,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ConnectionManagerConfirm,
		Description: "Connect",
		Handler:     cm.Confirm,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ConnectionManagerRetry,
		Description: "Retry",
		Handler:     cm.Retry,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ConnectionManagerAdd,
		Description: "Add connection",
		Handler:     cm.Add,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ConnectionManagerEdit,
		Description: "Edit connection/field",
		Handler:     cm.Edit,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ConnectionManagerFieldNext,
		Description: "Move form field focus forward",
		Handler:     cm.FieldNext,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ConnectionManagerFieldPrev,
		Description: "Move form field focus backward",
		Handler:     cm.FieldPrev,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ConnectionManagerFieldEdit,
		Description: "Edit form field",
		Handler:     cm.FieldEdit,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ConnectionManagerToggle,
		Description: "Toggle / cycle focused form field",
		Handler:     cm.Toggle,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ConnectionManagerDelete,
		Description: "Delete connection",
		Handler:     cm.Delete,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ConnectionManagerPasteDSN,
		Description: "Paste DSN as fields",
		Handler:     cm.PasteDSN,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ConnectionManagerTestConnection,
		Description: "Test connection",
		Handler:     cm.TestConnection,
	})
}

// AttachToContext registers GetKeybindings on the CONNECTION_MANAGER context.
func (cm *ConnectionManagerController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(cm.GetKeybindings)
}
