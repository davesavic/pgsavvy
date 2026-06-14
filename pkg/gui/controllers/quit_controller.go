package controllers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// QuitController owns the global quit + cheatsheet bindings.
//
// Bindings:
//   - <c-c>  : guarded quit (pending-edit gate, then open-tx gate)
//   - ?      : open the auto-generated cheatsheet (real impl ships
//     later — today the registered handler is a no-op stub so the
//     binding has a leaf to dispatch).
//
// The <leader>q chord is shipped as a default in
// config.GetDefaultConfig() and routes through the user-config layering
// path at Build time; this controller does not publish it.
//
// QuitController attaches to the GLOBAL_CONTEXT (no view); bindings
// flow through the runtime's global keybinding pass.
type QuitController struct {
	baseController
}

// NewQuitController constructs the controller. UIDeps and QueryDeps are
// needed for the tx-open confirmation dialog; EditDeps carries
// the PendingDiscardHelper for the pending-edit gate.
func NewQuitController(
	c *common.Common,
	core CoreDeps,
	ui UIDeps,
	query QueryDeps,
	edit EditDeps,
) *QuitController {
	return &QuitController{
		baseController: newBase(c, HelperBag{
			CoreDeps:  core,
			UIDeps:    ui,
			QueryDeps: query,
			EditDeps:  edit,
		}),
	}
}

// Quit terminates the gocui main loop after checking guards:
//  1. Pending-edit gate (BlockQuitIfPending) — toast and abort if edits are pending.
//  2. Open-transaction gate — 3-choice dialog: commit/rollback/abort.
//
// If no guards fire, returns gocui.ErrQuit immediately.
func (q *QuitController) Quit(_ commands.ExecCtx) error {
	// Guard 1: pending edits (same check :q runs in the ex handler).
	if pd := q.helpers.PendingDiscard; pd != nil {
		if err := pd.BlockQuitIfPending(); err != nil {
			if q.helpers.Toast != nil {
				q.helpers.Toast.Show(err.Error(), 4*time.Second)
			}
			return nil
		}
	}

	// Guard 2: open transaction.
	runner := q.helpers.QueryRunner
	if runner != nil && runner.InTransaction() {
		return q.showTxQuitDialog()
	}

	return gocui.ErrQuit
}

// showTxQuitDialog pushes the 3-choice selection popup: commit-and-quit,
// rollback-and-quit, or abort. The dialog body shows the statement count
// and any active savepoint names.
func (q *QuitController) showTxQuitDialog() error {
	if q.helpers.Choice == nil {
		// No ChoiceHelper wired — fall through to quit.
		return gocui.ErrQuit
	}

	runner := q.helpers.QueryRunner

	// Build the dialog body.
	stmtCount := runner.TxStatementCount()
	savepoints := runner.SavepointNames()

	var body strings.Builder
	body.WriteString(fmt.Sprintf("Transaction open (%d statement", stmtCount))
	if stmtCount != 1 {
		body.WriteByte('s')
	}
	body.WriteByte(')')
	if len(savepoints) > 0 {
		body.WriteString(fmt.Sprintf("\nSavepoints: %s", strings.Join(savepoints, ", ")))
	}

	choices := []string{
		"[c] Commit and quit",
		"[r] Rollback and quit",
		"[a] Abort (stay)",
	}

	return q.helpers.Choice.Choose(body.String(), choices, func(idx int) error {
		switch idx {
		case 0: // commit and quit
			return q.commitAndQuit()
		case 1: // rollback and quit
			return q.rollbackAndQuit()
		default: // abort — stay
			return nil
		}
	}, func() error {
		// Esc / cancel — same as abort.
		return nil
	})
}

// commitAndQuit cancels any in-flight stream, commits the transaction,
// and quits. If commit fails the dialog is re-shown with an error
// message prepended (the transaction remains open for the user to
// retry or rollback).
func (q *QuitController) commitAndQuit() error {
	runner := q.helpers.QueryRunner
	if runner == nil {
		return gocui.ErrQuit
	}

	runner.CancelAndWaitActiveRun()

	tx := runner.CurrentTransaction()
	if tx == nil {
		// Transaction vanished (e.g. connection died) — just quit.
		return gocui.ErrQuit
	}

	if err := tx.Commit(context.Background()); err != nil {
		// Commit failed: re-show the dialog with error context.
		if q.helpers.Toast != nil {
			q.helpers.Toast.Show(fmt.Sprintf("commit failed: %v", err), 4*time.Second)
		}
		return q.showTxQuitDialog()
	}

	return gocui.ErrQuit
}

// rollbackAndQuit cancels any in-flight stream, rolls back the
// transaction, and quits. If rollback fails (dead connection), quit
// proceeds anyway — the connection is already lost.
func (q *QuitController) rollbackAndQuit() error {
	runner := q.helpers.QueryRunner
	if runner == nil {
		return gocui.ErrQuit
	}

	runner.CancelAndWaitActiveRun()

	tx := runner.CurrentTransaction()
	if tx == nil {
		return gocui.ErrQuit
	}

	if err := tx.Rollback(context.Background()); err != nil {
		// Rollback failed (dead conn etc.) — quit anyway per AC.
		if q.helpers.Toast != nil {
			q.helpers.Toast.Show(fmt.Sprintf("rollback failed (quitting anyway): %v", err), 4*time.Second)
		}
	}

	return gocui.ErrQuit
}

// GetKeybindings returns the global quit / cheatsheet bindings.
func (q *QuitController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := q.tr()
	return []*types.ChordBinding{
		{
			Sequence:    []types.ChordKey{{Code: 'c', Mod: types.ChordModCtrl}},
			Mode:        types.ModeNormal,
			Scope:       types.GLOBAL,
			ActionID:    commands.AppQuit,
			Description: tr.Actions.QuitApp,
		},
		{
			Sequence:    []types.ChordKey{{Code: '?'}},
			Mode:        types.ModeNormal,
			Scope:       types.GLOBAL,
			ActionID:    commands.HelpCheatsheet,
			Description: tr.Actions.ShowMenu,
		},
		// <leader>C opens the connection manager mid-session.
		{
			Sequence:    []types.ChordKey{{Special: types.KeyLeader}, {Code: 'C'}},
			Mode:        types.ModeNormal,
			Scope:       types.GLOBAL,
			ActionID:    commands.ConnectionManagerOpen,
			Description: tr.Actions.OpenConnectionManager,
		},
	}
}

// RegisterActions registers the rail-specific action handlers this
// controller owns with reg. Trait actions and rail-switch actions are
// registered once at the Controllers aggregate level.
func (q *QuitController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	_ = reg.Register(&commands.Command{
		ID:          commands.AppQuit,
		Description: "Quit application",
		Handler:     q.Quit,
	})
}

// AttachToContext registers GetKeybindings on the supplied context
// (typically the GLOBAL context).
func (q *QuitController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(q.GetKeybindings)
}
