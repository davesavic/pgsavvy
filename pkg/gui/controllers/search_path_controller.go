package controllers

import (
	"strings"
	"time"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// searchPathToastTTL is the lifetime of toasts the SearchPathController surfaces.
const searchPathToastTTL = 4 * time.Second

// SearchPathController owns the <leader>p GLOBAL-scope binding
// that opens a prompt pre-filled with "SET search_path TO ". On submit
// the full text is delegated to the existing SET handler via SetRunner.
type SearchPathController struct {
	baseController

	// SetRunner is wired by the orchestrator to the setExHandler closure
	// The controller calls it with the tokenised args after "SET"
	// (e.g. ["search_path", "TO", "public,myschema"]) and a zero ExecCtx.
	SetRunner func(args []string, ctx commands.ExecCtx) error
}

// NewSearchPathController constructs the controller.
func NewSearchPathController(
	c *common.Common,
	core CoreDeps,
	nav NavDeps,
	ui UIDeps,
	query QueryDeps,
	threading ThreadingDeps,
) *SearchPathController {
	return &SearchPathController{
		baseController: newBase(c, HelperBag{
			CoreDeps:      core,
			NavDeps:       nav,
			UIDeps:        ui,
			QueryDeps:     query,
			ThreadingDeps: threading,
		}),
	}
}

// handleSearchPath is the <leader>p handler. It opens a prompt pre-filled
// with "SET search_path TO "; on submit the full text is split and
// delegated to SetRunner (the existing SET handler).
func (sp *SearchPathController) handleSearchPath(_ commands.ExecCtx) error {
	runner := sp.helpers.QueryRunner
	if runner == nil || !runner.HasSession() {
		sp.toast("no active connection")
		return nil
	}
	if sp.helpers.Prompt == nil {
		return nil
	}
	if sp.SetRunner == nil {
		sp.toast("SET handler not wired")
		return nil
	}

	return sp.helpers.Prompt.Prompt("search_path", "SET search_path TO ", func(value string) error {
		// Strip the leading "SET " (case-insensitive) to produce the
		// args slice the setExHandler expects.
		trimmed := value
		if len(trimmed) >= 4 && strings.EqualFold(trimmed[:4], "SET ") {
			trimmed = trimmed[4:]
		}
		args := strings.Fields(trimmed)
		if len(args) == 0 {
			sp.toast("empty SET statement")
			return nil
		}
		return sp.SetRunner(args, commands.ExecCtx{})
	}, nil)
}

// toast shows a transient message.
func (sp *SearchPathController) toast(msg string) {
	if sp.helpers.Toast == nil {
		return
	}
	sp.helpers.Toast.Show(msg, searchPathToastTTL)
}

// GetKeybindings publishes the <leader>p binding under GLOBAL scope.
func (sp *SearchPathController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := sp.tr()
	seq, err := keys.SequenceFromShorthand("<leader>p")
	if err != nil {
		return nil
	}
	return []*types.ChordBinding{
		{
			Sequence:    seq,
			Mode:        types.ModeNormal,
			Scope:       types.GLOBAL,
			ActionID:    commands.SearchPathQuickSet,
			Description: tr.Actions.SearchPathQuickSet,
		},
	}
}

// RegisterActions registers the search_path quick-set action.
func (sp *SearchPathController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	_ = reg.Register(&commands.Command{
		ID:          commands.SearchPathQuickSet,
		Description: "Set search_path via prompt",
		Tag:         "Session",
		Handler:     sp.handleSearchPath,
	})
}

// AttachToContext registers GetKeybindings on the GLOBAL context.
func (sp *SearchPathController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(sp.GetKeybindings)
}
