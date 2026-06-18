package controllers

import (
	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// HistoryClose is the action ID for the HISTORY leaf's <esc> binding,
// which switches back to the editor tab (it no longer pops a popup).
// Navigation (j/k/gg/G) and <cr> confirm are owned by the embedded
// ListControllerTrait under per-rail action IDs; only the close handler
// is net-new here.
const HistoryClose = "history.close"

// HistoryController owns the HISTORY leaf bindings: j/k/gg/G navigate (via
// the embedded trait), <cr> inserts the selected row's SQL at the editor
// cursor then switches to the editor tab (it does NOT run the query), and
// <esc> switches to the editor tab. The HISTORY leaf stays on the focus
// stack as part of the QUERY_RAIL container — nothing is popped.
//
// Trait composition mirrors TablesController: the controller embeds a
// *ListControllerTrait[*guicontext.HistoryContext] and supplies a
// confirm callback that type-asserts the picker to the concrete context
// and reads Selected().
type HistoryController struct {
	*ListControllerTrait[*guicontext.HistoryContext]

	// switchToEditor flips the QUERY_RAIL container back to the editor tab.
	// Injected by the orchestrator (it closes over the container's
	// SetActiveTab). Nil-safe: an unwired controller simply does not switch.
	switchToEditor func()
}

// NewHistoryController constructs the controller. editor receives the
// inserted SQL; switchToEditor returns to the editor tab after a confirm/esc.
// All deps are nil-safe — every handler nil-checks on use so unit tests wire
// whichever subset they exercise.
func NewHistoryController(
	c *common.Common,
	core CoreDeps,
	ctx *guicontext.HistoryContext,
	editor EditorBufferReader,
	switchToEditor func(),
) *HistoryController {
	base := newBase(c, HelperBag{CoreDeps: core, QueryDeps: QueryDeps{EditorBuffer: editor}})
	ctrl := &HistoryController{switchToEditor: switchToEditor}
	confirm := func(_ commands.ExecCtx) error {
		return ctrl.confirm()
	}
	ctrl.ListControllerTrait = NewListControllerTrait(base, viewName(types.HISTORY), ctx, ctx, confirm)
	return ctrl
}

// confirm inserts the selected row's SQL at the editor cursor then switches
// back to the editor tab. An empty selection (or unwired editor) is a no-op.
// The query is NOT run.
func (c *HistoryController) confirm() error {
	row, ok := c.picker.Selected()
	if !ok {
		return nil
	}
	if c.helpers.EditorBuffer == nil {
		return nil
	}
	if err := c.helpers.EditorBuffer.InsertAtCursor(row.SQL); err != nil {
		return c.wrapErr("history.confirm", err)
	}
	c.switchTab()
	return nil
}

// Close switches back to the editor tab (the leaf is not popped). Safe when
// the switch hook is unwired (no-op).
func (c *HistoryController) Close(_ commands.ExecCtx) error {
	c.switchTab()
	return nil
}

// switchTab flips the container back to the editor tab. Nil-safe.
func (c *HistoryController) switchTab() {
	if c.switchToEditor == nil {
		return
	}
	c.switchToEditor()
}

// GetKeybindings returns the HISTORY-scope bindings: j/k/gg/G + <cr>
// from the trait's baseBindings, plus the <esc> close binding. No
// printable-character or on-change bindings are published.
func (c *HistoryController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := c.tr()
	out := c.baseBindings()
	out = append(out, &types.ChordBinding{
		Sequence:    []types.ChordKey{{Special: types.KeyEsc}},
		Mode:        types.ModeNormal,
		Scope:       types.HISTORY,
		ActionID:    HistoryClose,
		Description: tr.Actions.Cancel,
	})
	// QUERY_RAIL tab cycle (`]` next / `[` prev) under the HISTORY scope.
	out = append(out,
		&types.ChordBinding{
			Sequence:    []types.ChordKey{{Code: ']'}},
			Mode:        types.ModeNormal,
			Scope:       types.HISTORY,
			ActionID:    commands.QueryRailTabNext,
			Description: tr.Actions.QueryRailTabNext,
			ShowInBar:   true,
		},
		&types.ChordBinding{
			Sequence:    []types.ChordKey{{Code: '['}},
			Mode:        types.ModeNormal,
			Scope:       types.HISTORY,
			ActionID:    commands.QueryRailTabPrev,
			Description: tr.Actions.QueryRailTabPrev,
			ShowInBar:   true,
		},
	)
	return out
}

// RegisterActions registers the trait's navigation/confirm handlers plus
// the local close handler.
func (c *HistoryController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	c.ListControllerTrait.RegisterActions(reg)
	_ = reg.Register(&commands.Command{
		ID:          HistoryClose,
		Description: "Return to query editor tab",
		Handler:     c.Close,
	})
}

// AttachToContext registers GetKeybindings on the HISTORY context.
func (c *HistoryController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(c.GetKeybindings)
}
