package controllers

import (
	"fmt"
	"time"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/session"
)

const stmtTimeoutToastTTL = 4 * time.Second

// StatementTimeoutController owns the <leader>tt QUERY_EDITOR-scope
// binding (hq5.11) that prompts for a postgres-style duration, validates
// it, runs SET statement_timeout on the session, and persists the
// override to AppState.
type StatementTimeoutController struct {
	baseController

	// SetRunner is wired by the orchestrator to the setExHandler closure
	// (hq5.8). Called with the tokenised args after "SET".
	SetRunner func(args []string, ctx commands.ExecCtx) error

	// PersistTimeout is wired by the orchestrator to persist the validated
	// timeout to AppState.StatementTimeoutOverride[connID].
	PersistTimeout func(connID, timeout string)

	// ActiveConnID is wired by the orchestrator to return the current
	// connection ID.
	ActiveConnID func() string
}

// NewStatementTimeoutController constructs the controller.
func NewStatementTimeoutController(
	c *common.Common,
	core CoreDeps,
	nav NavDeps,
	ui UIDeps,
	query QueryDeps,
	threading ThreadingDeps,
) *StatementTimeoutController {
	return &StatementTimeoutController{
		baseController: newBase(c, HelperBag{
			CoreDeps:      core,
			NavDeps:       nav,
			UIDeps:        ui,
			QueryDeps:     query,
			ThreadingDeps: threading,
		}),
	}
}

func (st *StatementTimeoutController) handleSetTimeout(_ commands.ExecCtx) error {
	runner := st.helpers.QueryRunner
	if runner == nil || !runner.HasSession() {
		st.toast("no active connection")
		return nil
	}
	if st.helpers.Prompt == nil {
		return nil
	}
	if st.SetRunner == nil {
		st.toast("SET handler not wired")
		return nil
	}

	return st.helpers.Prompt.Prompt("statement timeout (e.g. 5min, 30s, 0)", "", func(value string) error {
		if value == "" {
			return nil
		}

		canonical, err := session.CanonicalizeStatementTimeout(value)
		if err != nil {
			st.toast(fmt.Sprintf("invalid timeout: %v", err))
			return nil
		}

		args := []string{"statement_timeout", "=", "'" + canonical + "'"}
		if err := st.SetRunner(args, commands.ExecCtx{}); err != nil {
			return nil
		}

		if st.PersistTimeout != nil && st.ActiveConnID != nil {
			connID := st.ActiveConnID()
			if connID != "" {
				if canonical == "0" {
					st.PersistTimeout(connID, "")
				} else {
					st.PersistTimeout(connID, canonical)
				}
			}
		}

		return nil
	}, nil)
}

func (st *StatementTimeoutController) toast(msg string) {
	if st.helpers.Toast == nil {
		return
	}
	st.helpers.Toast.Show(msg, stmtTimeoutToastTTL)
}

// GetKeybindings publishes the <leader>tt binding under QUERY_EDITOR scope.
func (st *StatementTimeoutController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := st.tr()
	seq, err := keys.SequenceFromShorthand("<leader>tt")
	if err != nil {
		return nil
	}
	return []*types.ChordBinding{
		{
			Sequence:    seq,
			Mode:        types.ModeNormal,
			Scope:       types.QUERY_EDITOR,
			ActionID:    commands.StatementTimeoutSet,
			Description: tr.Actions.StatementTimeoutSet,
		},
	}
}

// RegisterActions registers the statement timeout action.
func (st *StatementTimeoutController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	_ = reg.Register(&commands.Command{
		ID:          commands.StatementTimeoutSet,
		Description: "Set statement timeout via prompt",
		Tag:         "Session",
		Handler:     st.handleSetTimeout,
	})
}

// AttachToContext registers GetKeybindings on the supplied context.
func (st *StatementTimeoutController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(st.GetKeybindings)
}
