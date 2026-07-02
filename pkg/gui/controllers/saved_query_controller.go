package controllers

import (
	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// SavedQueryClose is the action ID for the SAVED_QUERY leaf's <esc> binding,
// which switches back to the editor tab (it no longer pops a popup).
// Navigation (j/k/gg/G) and <cr> confirm are owned by the embedded
// ListControllerTrait under per-rail action IDs; the close + delete handlers
// are net-new here.
const SavedQueryClose = "saved_query.close"

// SavedQueryController owns the SAVED_QUERY leaf bindings: j/k/gg/G navigate
// (via the embedded trait), <cr> inserts the selected row's SQL at the editor
// cursor then switches to the editor tab (it does NOT run the query), dd
// deletes the selected entry (after a confirmation, re-reading queries.yml),
// and <esc> switches to the editor tab. The SAVED_QUERY leaf stays on the
// focus stack as part of the QUERY_RAIL container — nothing is popped.
type SavedQueryController struct {
	*ListControllerTrait[*guicontext.SavedQueryContext]

	// switchToEditor flips the QUERY_RAIL container back to the editor tab.
	// Injected by the orchestrator (it closes over the container's
	// SetActiveTab). Nil-safe.
	switchToEditor func()

	fs          afero.Fs
	queriesPath string
}

// NewSavedQueryController constructs the controller. editor receives the
// inserted SQL; switchToEditor returns to the editor tab after a confirm/esc;
// confirm gates the dd delete; fs + queriesPath address queries.yml for the
// delete/refresh. All deps are nil-safe — every handler nil-checks on use so
// unit tests wire whichever subset they exercise.
func NewSavedQueryController(
	c *common.Common,
	core CoreDeps,
	ui UIDeps,
	ctx *guicontext.SavedQueryContext,
	editor EditorBufferReader,
	switchToEditor func(),
	fs afero.Fs,
	queriesPath string,
) *SavedQueryController {
	base := newBase(c, HelperBag{CoreDeps: core, UIDeps: ui, QueryDeps: QueryDeps{EditorBuffer: editor}})
	ctrl := &SavedQueryController{
		switchToEditor: switchToEditor,
		fs:             fs,
		queriesPath:    queriesPath,
	}
	confirm := func(_ commands.ExecCtx) error {
		return ctrl.confirm()
	}
	ctrl.ListControllerTrait = NewListControllerTrait(base, viewName(types.SAVED_QUERY), ctx, ctx, confirm)
	return ctrl
}

// confirm inserts the selected row's SQL at the editor cursor then switches
// back to the editor tab. An empty selection (or unwired editor) is a no-op.
// The query is NOT run.
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
	c.switchTab()
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
// list authoritative. The list is marked stale so any other surface (e.g. a
// future re-entry) reloads, and the refresh here keeps the on-screen list
// in sync without zeroing the cursor.
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
	c.picker.MarkStale()
	c.picker.RefreshRows(rows)
	return nil
}

// Close switches back to the editor tab (the leaf is not popped). Safe when
// the switch hook is unwired (no-op).
func (c *SavedQueryController) Close(_ commands.ExecCtx) error {
	c.switchTab()
	return nil
}

// switchTab flips the container back to the editor tab. Nil-safe.
func (c *SavedQueryController) switchTab() {
	if c.switchToEditor == nil {
		return
	}
	c.switchToEditor()
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
		// QUERY_RAIL tab cycle (`]` next / `[` prev) under the SAVED_QUERY scope.
		&types.ChordBinding{
			Sequence:    []types.ChordKey{{Code: ']'}},
			Mode:        types.ModeNormal,
			Scope:       types.SAVED_QUERY,
			ActionID:    commands.QueryRailTabNext,
			Description: tr.Actions.QueryRailTabNext,
			ShowInBar:   true,
		},
		&types.ChordBinding{
			Sequence:    []types.ChordKey{{Code: '['}},
			Mode:        types.ModeNormal,
			Scope:       types.SAVED_QUERY,
			ActionID:    commands.QueryRailTabPrev,
			Description: tr.Actions.QueryRailTabPrev,
			ShowInBar:   true,
		},
	)
	out = append(out, railSwitchBindings(string(types.SAVED_QUERY), tr)...)
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
		Description: "Return to query editor tab",
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
