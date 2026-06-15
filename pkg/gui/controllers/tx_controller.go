package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// txToastTTL is the lifetime of toasts the TxController surfaces.
const txToastTTL = 4 * time.Second

// TxController owns the <leader>t transaction-submenu bindings under
// QUERY_EDITOR scope: Begin, Commit, Rollback, Savepoint,
// ReleaseSavepoint, RollbackToSavepoint. Handlers delegate to the
// QueryRunner's Transaction API.
type TxController struct {
	baseController
}

// NewTxController constructs the controller. c may be nil (tests).
func NewTxController(c *common.Common, core CoreDeps, nav NavDeps, ui UIDeps, query QueryDeps) *TxController {
	return &TxController{
		baseController: newBase(c, HelperBag{CoreDeps: core, NavDeps: nav, UIDeps: ui, QueryDeps: query}),
	}
}

// GetKeybindings publishes the transaction-submenu bindings under
// QUERY_EDITOR scope. All are <leader>t-prefixed chords.
func (tx *TxController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := tx.tr()
	type bspec struct {
		shorthand   string
		actionID    string
		description string
	}
	specs := []bspec{
		{"<leader>tb", commands.TxBegin, tr.Actions.TxBegin},
		{"<leader>tc", commands.TxCommit, tr.Actions.TxCommit},
		{"<leader>tr", commands.TxRollback, tr.Actions.TxRollback},
		{"<leader>ts", commands.TxSavepoint, tr.Actions.TxSavepoint},
		{"<leader>tR", commands.TxReleaseSavepoint, tr.Actions.TxReleaseSavepoint},
		{"<leader>to", commands.TxRollbackToSavepoint, tr.Actions.TxRollbackToSavepoint},
	}
	out := make([]*types.ChordBinding, 0, len(specs))
	for _, s := range specs {
		seq, err := keys.SequenceFromShorthand(s.shorthand)
		if err != nil {
			continue
		}
		out = append(out, &types.ChordBinding{
			Sequence:    seq,
			Mode:        types.ModeNormal,
			Scope:       types.QUERY_EDITOR,
			ActionID:    s.actionID,
			Description: s.description,
		})
	}
	return out
}

// RegisterActions registers the six transaction commands with reg.
// Begin is disabled when already in a transaction; the remaining five
// are disabled when no transaction is active.
func (tx *TxController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	runner := tx.helpers.QueryRunner

	_ = reg.Register(&commands.Command{
		ID:          commands.TxBegin,
		Description: "Begin a transaction",
		Tag:         "Transaction",
		Handler:     tx.handleBegin,
		GetDisabled: func(commands.ExecCtx) (string, bool) {
			if runner != nil && runner.InTransaction() {
				return "already in a transaction", true
			}
			return "", false
		},
	})

	noTxDisabled := func(commands.ExecCtx) (string, bool) {
		if runner == nil || !runner.InTransaction() {
			return "no active transaction", true
		}
		return "", false
	}

	_ = reg.Register(&commands.Command{
		ID:          commands.TxCommit,
		Description: "Commit the active transaction",
		Tag:         "Transaction",
		Handler:     tx.handleCommit,
		GetDisabled: noTxDisabled,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.TxRollback,
		Description: "Rollback the active transaction",
		Tag:         "Transaction",
		Handler:     tx.handleRollback,
		GetDisabled: noTxDisabled,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.TxSavepoint,
		Description: "Create a savepoint",
		Tag:         "Transaction",
		Handler:     tx.handleSavepoint,
		GetDisabled: noTxDisabled,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.TxReleaseSavepoint,
		Description: "Release a savepoint",
		Tag:         "Transaction",
		Handler:     tx.handleReleaseSavepoint,
		GetDisabled: noTxDisabled,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.TxRollbackToSavepoint,
		Description: "Rollback to a savepoint",
		Tag:         "Transaction",
		Handler:     tx.handleRollbackToSavepoint,
		GetDisabled: noTxDisabled,
	})
}

// AttachToContext registers GetKeybindings on the supplied context.
func (tx *TxController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(tx.GetKeybindings)
}

// --- Handlers ---

func (tx *TxController) handleBegin(_ commands.ExecCtx) error {
	runner := tx.helpers.QueryRunner
	if runner == nil || !runner.HasSession() {
		tx.toast("no active connection")
		return nil
	}
	// preemptInFlight is called inside runner.Begin.
	if _, err := runner.Begin(context.Background(), models.TxOptions{}); err != nil {
		tx.toast(fmt.Sprintf("begin failed: %v", err))
		return nil
	}
	tx.toast("transaction started")
	return nil
}

func (tx *TxController) handleCommit(_ commands.ExecCtx) error {
	runner := tx.helpers.QueryRunner
	if runner == nil {
		return nil
	}
	cur := runner.CurrentTransaction()
	if cur == nil {
		tx.toast("no active transaction")
		return nil
	}
	if err := cur.Commit(context.Background()); err != nil {
		tx.toast(fmt.Sprintf("commit failed: %v", err))
		return nil
	}
	tx.toast("transaction committed")
	return nil
}

func (tx *TxController) handleRollback(_ commands.ExecCtx) error {
	runner := tx.helpers.QueryRunner
	if runner == nil {
		return nil
	}
	cur := runner.CurrentTransaction()
	if cur == nil {
		tx.toast("no active transaction")
		return nil
	}
	if err := cur.Rollback(context.Background()); err != nil {
		tx.toast(fmt.Sprintf("rollback failed: %v", err))
		return nil
	}
	tx.toast("transaction rolled back")
	return nil
}

func (tx *TxController) handleSavepoint(_ commands.ExecCtx) error {
	runner := tx.helpers.QueryRunner
	if runner == nil {
		return nil
	}
	if tx.helpers.Prompt == nil {
		return nil
	}
	return tx.helpers.Prompt.Prompt("savepoint name", "", func(name string) error {
		if name == "" {
			tx.toast("savepoint name cannot be empty")
			return nil
		}
		cur := runner.CurrentTransaction()
		if cur == nil {
			tx.toast("no active transaction")
			return nil
		}
		if err := cur.Savepoint(context.Background(), name); err != nil {
			tx.toast(fmt.Sprintf("savepoint failed: %v", err))
			return nil
		}
		tx.toast(fmt.Sprintf("savepoint %s created", name))
		return nil
	}, nil)
}

func (tx *TxController) handleReleaseSavepoint(_ commands.ExecCtx) error {
	runner := tx.helpers.QueryRunner
	if runner == nil {
		return nil
	}
	cur := runner.CurrentTransaction()
	if cur == nil {
		tx.toast("no active transaction")
		return nil
	}
	sps := cur.Savepoints()
	if len(sps) == 0 {
		tx.toast("no savepoints")
		return nil
	}
	if tx.helpers.Choice == nil {
		return nil
	}
	return tx.helpers.Choice.Choose("release savepoint", sps, func(idx int) error {
		name := sps[idx]
		if err := cur.Release(context.Background(), name); err != nil {
			tx.toast(fmt.Sprintf("release failed: %v", err))
			return nil
		}
		tx.toast(fmt.Sprintf("savepoint %s released", name))
		return nil
	}, nil)
}

func (tx *TxController) handleRollbackToSavepoint(_ commands.ExecCtx) error {
	runner := tx.helpers.QueryRunner
	if runner == nil {
		return nil
	}
	cur := runner.CurrentTransaction()
	if cur == nil {
		tx.toast("no active transaction")
		return nil
	}
	sps := cur.Savepoints()
	if len(sps) == 0 {
		tx.toast("no savepoints")
		return nil
	}
	if tx.helpers.Choice == nil {
		return nil
	}
	return tx.helpers.Choice.Choose("rollback to savepoint", sps, func(idx int) error {
		name := sps[idx]
		if err := cur.RollbackTo(context.Background(), name); err != nil {
			tx.toast(fmt.Sprintf("rollback to savepoint failed: %v", err))
			return nil
		}
		tx.toast(fmt.Sprintf("rolled back to savepoint %s", name))
		return nil
	}, nil)
}

// toast shows a transient message via the Toast helper.
func (tx *TxController) toast(msg string) {
	if tx.helpers.Toast == nil {
		return
	}
	tx.helpers.Toast.Show(msg, txToastTTL)
}
