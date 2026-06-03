package controllers

import (
	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// HistoryClose is the action ID for the HISTORY popup's <esc> close
// binding. Navigation (j/k/gg/G) and <cr> confirm are owned by the
// embedded ListControllerTrait under per-rail action IDs; only the
// close handler is net-new here.
const HistoryClose = "history.close"

// historyTree is the narrow focus-stack surface the controller uses to
// dismiss the popup. The orchestrator's *gui.ContextTree satisfies it.
// Mirrors fkReverseTree in fk_reverse_picker_controller.go.
type historyTree interface {
	Pop() error
}

// HistoryController owns the HISTORY popup bindings: j/k/gg/G navigate
// (via the embedded trait), <cr> inserts the selected row's SQL at the
// editor cursor + pops the popup + refocuses the query editor (it does
// NOT run the query), and <esc> pops the popup.
//
// Trait composition mirrors TablesController: the controller embeds a
// *ListControllerTrait[*guicontext.HistoryContext] and supplies a
// confirm callback that type-asserts the picker to the concrete context
// and reads Selected().
type HistoryController struct {
	*ListControllerTrait[*guicontext.HistoryContext]

	tree    historyTree
	refocus func() error
}

// NewHistoryController constructs the controller. tree dismisses the
// popup; editor receives the inserted SQL; refocus returns focus to the
// query editor after a successful insert. All deps are nil-safe — every
// handler nil-checks on use so unit tests wire whichever subset they
// exercise.
func NewHistoryController(
	c *common.Common,
	core CoreDeps,
	ctx *guicontext.HistoryContext,
	editor EditorBufferReader,
	tree historyTree,
	refocus func() error,
) *HistoryController {
	base := newBase(c, HelperBag{CoreDeps: core, QueryDeps: QueryDeps{EditorBuffer: editor}})
	ctrl := &HistoryController{tree: tree, refocus: refocus}
	confirm := func(_ commands.ExecCtx) error {
		return ctrl.confirm()
	}
	ctrl.ListControllerTrait = NewListControllerTrait(base, viewName(types.HISTORY), ctx, ctx, confirm)
	return ctrl
}

// confirm inserts the selected row's SQL at the editor cursor, pops the
// popup, and refocuses the query editor. An empty selection (or unwired
// editor) is a no-op. The query is NOT run.
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
	if c.tree != nil {
		_ = c.tree.Pop()
	}
	if c.refocus != nil {
		_ = c.refocus()
	}
	return nil
}

// Close pops the popup off the focus stack. Safe when the tree is
// unwired (no-op).
func (c *HistoryController) Close(_ commands.ExecCtx) error {
	if c.tree == nil {
		return nil
	}
	_ = c.tree.Pop()
	return nil
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
		Description: "Close history popup",
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
