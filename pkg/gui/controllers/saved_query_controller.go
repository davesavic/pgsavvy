package controllers

import (
	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// SavedQueryClose is the action ID for the SAVED_QUERY popup's <esc> close
// binding. Navigation (j/k/gg/G) and <cr> confirm are owned by the embedded
// ListControllerTrait under per-rail action IDs; the close + delete handlers
// are net-new here.
const SavedQueryClose = "saved_query.close"

// savedQueryTree is the narrow focus-stack surface the controller uses to
// dismiss the popup. The orchestrator's *gui.ContextTree satisfies it.
// Mirrors historyTree.
type savedQueryTree interface {
	Pop() error
}

// SavedQueryController owns the SAVED_QUERY popup bindings: j/k/gg/G
// navigate (via the embedded trait), <cr> inserts the selected row's SQL at
// the editor cursor + pops the popup + refocuses the query editor (it does
// NOT run the query), dd deletes the selected entry (after a confirmation,
// re-reading queries.yml), and <esc> pops the popup.
type SavedQueryController struct {
	*ListControllerTrait[*guicontext.SavedQueryContext]

	tree    savedQueryTree
	refocus func() error

	fs          afero.Fs
	queriesPath string
}

// NewSavedQueryController constructs the controller. tree dismisses the
// popup; editor receives the inserted SQL; confirm gates the dd delete; fs +
// queriesPath address queries.yml for the delete/refresh. refocus returns
// focus to the query editor after a successful insert. All deps are nil-safe
// — every handler nil-checks on use so unit tests wire whichever subset they
// exercise.
func NewSavedQueryController(
	c *common.Common,
	core CoreDeps,
	ui UIDeps,
	ctx *guicontext.SavedQueryContext,
	editor EditorBufferReader,
	tree savedQueryTree,
	refocus func() error,
	fs afero.Fs,
	queriesPath string,
) *SavedQueryController {
	base := newBase(c, HelperBag{CoreDeps: core, UIDeps: ui, QueryDeps: QueryDeps{EditorBuffer: editor}})
	ctrl := &SavedQueryController{
		tree:        tree,
		refocus:     refocus,
		fs:          fs,
		queriesPath: queriesPath,
	}
	confirm := func(_ commands.ExecCtx) error {
		return ctrl.confirm()
	}
	ctrl.ListControllerTrait = NewListControllerTrait(base, viewName(types.SAVED_QUERY), ctx, ctx, confirm)
	return ctrl
}

// confirm inserts the selected row's SQL at the editor cursor, pops the
// popup, and refocuses the query editor. An empty selection (or unwired
// editor) is a no-op. The query is NOT run.
func (c *SavedQueryController) confirm() error {
	row, ok := c.picker.Selected()
	if !ok {
		return nil
	}
	if c.helpers.EditorBuffer == nil {
		return nil
	}
	if err := c.helpers.EditorBuffer.InsertAtCursor(row.SQL); err != nil {
		return c.wrapErr("saved_query.confirm", err)
	}
	if c.tree != nil {
		_ = c.tree.Pop()
	}
	if c.refocus != nil {
		_ = c.refocus()
	}
	return nil
}

// delete confirms, then removes the selected entry from queries.yml and
// re-reads the file so the picker reflects the on-disk truth. An empty
// selection is a no-op (no Confirm, no panic). The cursor is clamped (not
// zeroed) so it lands on a sensible neighbour after the row is gone.
func (c *SavedQueryController) delete(_ commands.ExecCtx) error {
	row, ok := c.picker.Selected()
	if !ok {
		return nil
	}
	if c.helpers.Confirm == nil {
		return nil
	}
	return c.helpers.Confirm.Confirm(
		"Delete saved query",
		"Delete \""+row.Name+"\"?",
		func() error { return c.doDelete(row.Name) },
		nil,
	)
}

// doDelete performs the on-disk delete and refreshes the picker from
// queries.yml. Re-reading (rather than mutating the open snapshot) keeps the
// list authoritative.
func (c *SavedQueryController) doDelete(name string) error {
	if c.fs == nil {
		return nil
	}
	if err := config.DeleteQuery(c.fs, c.queriesPath, name); err != nil {
		return c.wrapErr("saved_query.delete", err)
	}
	rows, err := config.LoadQueries(c.fs, c.queriesPath)
	if err != nil {
		return c.wrapErr("saved_query.refresh", err)
	}
	c.picker.RefreshRows(rows)
	return nil
}

// Close pops the popup off the focus stack. Safe when the tree is unwired
// (no-op).
func (c *SavedQueryController) Close(_ commands.ExecCtx) error {
	if c.tree == nil {
		return nil
	}
	_ = c.tree.Pop()
	return nil
}

// GetKeybindings returns the SAVED_QUERY-scope bindings: j/k/gg/G + <cr>
// from the trait's baseBindings, plus the dd delete and <esc> close
// bindings. No printable-character or on-change bindings are published.
func (c *SavedQueryController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := c.tr()
	out := c.baseBindings()
	out = append(out,
		&types.ChordBinding{
			Sequence:    []types.ChordKey{{Code: 'd'}, {Code: 'd'}},
			Mode:        types.ModeNormal,
			Scope:       types.SAVED_QUERY,
			ActionID:    commands.QuerySavedDelete,
			Description: tr.Actions.DeleteSavedQuery,
		},
		&types.ChordBinding{
			Sequence:    []types.ChordKey{{Special: types.KeyEsc}},
			Mode:        types.ModeNormal,
			Scope:       types.SAVED_QUERY,
			ActionID:    SavedQueryClose,
			Description: tr.Actions.Cancel,
		},
	)
	return out
}

// RegisterActions registers the trait's navigation/confirm handlers plus the
// local delete + close handlers.
func (c *SavedQueryController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	c.ListControllerTrait.RegisterActions(reg)
	_ = reg.Register(&commands.Command{
		ID:          commands.QuerySavedDelete,
		Description: "Delete saved query",
		Tag:         "Query",
		Handler:     c.delete,
	})
	_ = reg.Register(&commands.Command{
		ID:          SavedQueryClose,
		Description: "Close saved-query popup",
		Handler:     c.Close,
	})
}

// AttachToContext registers GetKeybindings on the SAVED_QUERY context.
func (c *SavedQueryController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(c.GetKeybindings)
}
