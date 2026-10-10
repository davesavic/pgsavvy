package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/davesavic/pgsavvy/pkg/gui"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/logs"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// commitDialogDeps bundles every collaborator the apply / dry-run /
// show-sql hooks need.
type commitDialogDeps struct {
	apply       *helpers.CellApplyHelper
	tabs        *ui.ResultTabsHelper
	conflictCtx *guicontext.ConflictDialogContext
	tree        *gui.ContextTree
	toast       *ui.ToastHelper
	logger      *slog.Logger
	// onUI schedules a closure on the gocui MainLoop. The conflict-dialog
	// push must defer through this — pushing inline would race with the
	// commit dialog's own Close + Pop that runs immediately after this
	// hook returns, popping the conflict dialog instead of the commit one.
	onUI func(fn func() error)
	// evictCounts drops the relationship panel's cached inbound counts for a
	// (schema, table) child table after a successful commit. Resolves the panel
	// controller lazily (it is wired AFTER the commit dialog) and is nil-safe, so
	// commit works whether or not the panel exists. Never nil once wired (a
	// no-op closure stands in).
	evictCounts func(schema, table string)
}

// commitApplyHook implements controllers.CommitDialogApplyHook.
type commitApplyHook struct{ deps commitDialogDeps }

func (h commitApplyHook) Apply(set *models.PendingEditSet, conn *models.Connection) error {
	pkCols, err := capturedPKColumns(h.deps.tabs, set)
	if err != nil {
		return err
	}
	snapshot, err := set.BeginCommit(nil)
	if err != nil {
		return err
	}
	res, conflicts, err := h.deps.apply.Apply(context.Background(), snapshot, pkCols, false)
	set.SettleCommit(snapshot, res.Committed, res.CommitUnknown, res.OriginalPKs, res.ResultPKs)
	if err != nil && !res.Committed {
		return err
	}
	if len(conflicts) > 0 {
		if h.deps.conflictCtx == nil || h.deps.tree == nil {
			return fmt.Errorf("commit dialog: conflict dialog not wired (%d conflicts)", len(conflicts))
		}
		if openErr := h.deps.conflictCtx.Open(conflicts, conn); openErr != nil {
			return openErr
		}
		if h.deps.onUI == nil {
			return errors.New("commit dialog: UI scheduler not wired")
		}
		conflictCtx, tree := h.deps.conflictCtx, h.deps.tree
		h.deps.onUI(func() error { return tree.Push(conflictCtx) })
		return nil
	}
	rows := len(res.ResultPKs)
	set.RetireCommitted(res.Edits, res.OriginalPKs, res.ResultPKs)
	// A committed UPDATE on this table can change which child rows reference a
	// given parent key, so the relationship panel's cached inbound counts keyed
	// by this (child) table are now stale. Evict per-table so the next focus
	// recomputes — even if the panel is currently closed (the cache survives
	// close). FK metadata itself is unaffected by DML.
	if h.deps.evictCounts != nil {
		h.deps.evictCounts(set.Table.Schema, set.Table.Table)
	}
	// Write the post-commit server values back into the grid so the
	// applied cells render fresh data instead of the original load
	// snapshot.
	refreshCommittedGrid(h.deps.tabs, res)
	if err != nil {
		showCommitRefreshWarning(h.deps.toast, h.deps.logger)
		return nil
	}
	if h.deps.toast != nil {
		h.deps.toast.Show(fmt.Sprintf("%d row(s) committed", rows), 0)
	}
	return nil
}

func showCommitRefreshWarning(toast *ui.ToastHelper, logger *slog.Logger) {
	if toast != nil {
		toast.Show("Writes committed, but display refresh failed. Reload the result; do not reapply committed edits.", 0)
	}
	if logger != nil {
		logs.Event(logger, "warn", "commit_refresh_failed", slog.Bool("committed", true))
	}
}

func refreshCommittedGrid(tabs *ui.ResultTabsHelper, res helpers.ApplyResult) {
	if tabs == nil {
		return
	}
	tab := tabs.Active()
	if tab == nil || tab.Grid() == nil {
		return
	}
	tab.Grid().UpdateRowsFromOriginalPK(res.Identity, res.RefetchedColumns, res.RefetchedRows, res.RefetchedOriginalPKs)
}

// refreshGridRows writes refetched server rows back into the active
// tab's grid via UpdateRowsByPK. Nil-safe at every hop — a missing tab
// or grid degrades to "grid keeps its loaded values", never an error:
// by the time callers reach here the server-side work has already
// committed, so display refresh must not fail the operation.
func refreshGridRows(tabs *ui.ResultTabsHelper, cols []models.ColumnMeta, rows []models.Row) {
	if tabs == nil {
		return
	}
	tab := tabs.Active()
	if tab == nil {
		return
	}
	g := tab.Grid()
	if g == nil {
		return
	}
	g.UpdateRowsByPK(cols, rows)
}

// commitShowSqlHook implements controllers.CommitDialogShowSqlHook.
// Emits a one-shot audit line per ADR-28 each time the body flips into
// SqlPreview mode.
type commitShowSqlHook struct{ logger *slog.Logger }

func (h commitShowSqlHook) OnShowSQL(set *models.PendingEditSet, conn *models.Connection) {
	if h.logger == nil || set == nil {
		return
	}
	connName := ""
	if conn != nil {
		connName = conn.Name
	}
	logs.Event(h.logger, "audit", "commit_dialog_show_sql",
		slog.String("conn", connName),
		slog.String("schema", set.Table.Schema),
		slog.String("table", set.Table.Table),
		slog.Int("edits", set.Count()),
	)
}

// conflictDialogDeps bundles the collaborators the conflict-dialog
// refresh / overwrite hooks need. Sibling to commitDialogDeps —
// separated so the conflict dialog can be wired independently of the
// commit dialog's onUI + conflictCtx fields.
type conflictDialogDeps struct {
	apply         *helpers.CellApplyHelper
	tabs          *ui.ResultTabsHelper
	toast         *ui.ToastHelper
	activeSetFunc func() *models.PendingEditSet
	// evictCounts drops the relationship panel's cached inbound counts for the
	// overwritten (schema, table) child table after a forced overwrite commits.
	// Same lazy-resolve + nil-safe contract as commitDialogDeps.evictCounts.
	evictCounts func(schema, table string)
}

// conflictRefreshHook implements controllers.ConflictDialogRefreshHook.
// Drops each conflicted edit from the active PendingEditSet and writes
// the conflict-time ServerValue into the grid cell, so the user sees the
// server's current data instead of the stale load snapshot once the
// dirty-cell substitution disappears with the dropped edit.
type conflictRefreshHook struct{ deps conflictDialogDeps }

func (h conflictRefreshHook) Refresh(conflicts []models.ConflictedEdit, _ *models.Connection) error {
	if h.deps.activeSetFunc == nil {
		return errors.New("conflict dialog: pending edit set resolver not wired")
	}
	set := h.deps.activeSetFunc()
	if set == nil {
		return errors.New("conflict dialog: no active pending edit set")
	}
	selected := make([]models.PendingEdit, len(conflicts))
	for i, c := range conflicts {
		selected[i] = c.Edit
	}
	if err := set.DiscardOwned(selected); err != nil {
		return err
	}
	writeServerValuesToGrid(h.deps.tabs, conflicts)
	if h.deps.toast != nil {
		h.deps.toast.Show(
			fmt.Sprintf("%d conflicting edit(s) dropped — showing current server values", len(conflicts)),
			0,
		)
	}
	return nil
}

// writeServerValuesToGrid surfaces each conflict's captured ServerValue
// in the active grid by synthesizing a one-row refetch projection
// ([pk..., conflicted column]) per conflict. One UpdateRowsByPK call per
// conflict — sequential calls compose when several conflicted columns
// share a row, whereas batching same-PK rows into one call would let the
// last row win. A failure to resolve the grid's PK columns degrades to
// "grid keeps its loaded values" — the edits are already dropped and the
// server was never written, so there is nothing to fail.
func writeServerValuesToGrid(tabs *ui.ResultTabsHelper, conflicts []models.ConflictedEdit) {
	if tabs == nil || tabs.Active() == nil || tabs.Active().Grid() == nil {
		return
	}
	for _, c := range conflicts {
		identity := c.Edit.Identity()
		if !identity.Compatible(tabs.Active().Grid().EditIdentity()) {
			continue
		}
		pkCols := identity.Names()
		if len(c.Edit.PrimaryKey) != len(pkCols) {
			continue
		}
		cols := make([]models.ColumnMeta, 0, len(pkCols)+1)
		vals := make([]any, 0, len(pkCols)+1)
		for i, name := range pkCols {
			cols = append(cols, models.ColumnMeta{Name: name})
			vals = append(vals, c.Edit.PrimaryKey[i])
		}
		cols = append(cols, models.ColumnMeta{Name: c.Edit.Column})
		vals = append(vals, c.ServerValue)
		refreshGridRows(tabs, cols, []models.Row{{Values: vals}})
	}
}

// conflictOverwriteHook implements controllers.ConflictDialogOverwriteHook.
// Re-issues each conflicted edit as a PK-only UPDATE via
// CellApplyHelper.Overwrite, then clears the staged edits on success.
// Gated upstream by ctx.OverwriteAllowed() — never fires on a
// confirm_writes:true connection.
type conflictOverwriteHook struct{ deps conflictDialogDeps }

func (h conflictOverwriteHook) Overwrite(conflicts []models.ConflictedEdit, _ *models.Connection) error {
	if h.deps.apply == nil {
		return errors.New("conflict dialog: cell apply helper not wired")
	}
	if h.deps.activeSetFunc == nil {
		return errors.New("conflict dialog: pending edit set resolver not wired")
	}
	set := h.deps.activeSetFunc()
	if set == nil {
		return errors.New("conflict dialog: no active pending edit set")
	}
	if set.CommitOutcomeUnknown() {
		return models.ErrCommitOutcomeUnknown
	}
	pkCols, err := capturedPKColumns(h.deps.tabs, set)
	if err != nil {
		return err
	}
	// Build an overwrite-only set containing just the conflicted edits.
	// Apply's transaction rolled back, so non-conflicting staged edits
	// are also un-applied; the conflict slice is the authoritative list
	// of what the user has chosen to force through, and the user will
	// re-run :w for the remaining non-conflicting edits.
	attempt := make([]models.PendingEdit, 0, len(conflicts))
	for _, c := range conflicts {
		attempt = append(attempt, c.Edit)
	}
	owSet, err := set.BeginCommit(attempt)
	if err != nil {
		return err
	}
	res, applyErr := h.deps.apply.Overwrite(context.Background(), owSet, pkCols)
	set.SettleCommit(owSet, res.Committed, res.CommitUnknown, res.OriginalPKs, res.ResultPKs)
	if applyErr != nil && !res.Committed {
		return applyErr
	}
	// Drop the overwritten edits from the live staged set.
	set.RetireCommitted(attempt, res.OriginalPKs, res.ResultPKs)
	// Evict the panel's cached inbound counts for the overwritten table — the
	// committed overwrite may change child references. Same per-table eviction
	// as the apply path.
	if h.deps.evictCounts != nil {
		h.deps.evictCounts(owSet.Table.Schema, owSet.Table.Table)
	}
	// Write the post-overwrite server rows back into the grid — same
	// refetch contract as the apply path.
	refreshCommittedGrid(h.deps.tabs, res)
	if applyErr != nil {
		showCommitRefreshWarning(h.deps.toast, nil)
		return nil
	}
	if h.deps.toast != nil {
		h.deps.toast.Show(
			fmt.Sprintf("%d row(s) overwritten", len(res.ResultPKs)),
			0,
		)
	}
	return nil
}

// capturedPKColumns checks the active view but never reinterprets staged values.
func capturedPKColumns(tabs *ui.ResultTabsHelper, set *models.PendingEditSet) ([]string, error) {
	if tabs == nil {
		return nil, errors.New("commit dialog: result tabs not wired")
	}
	tab := tabs.Active()
	if tab == nil {
		return nil, errors.New("commit dialog: no active result tab")
	}
	grid := tab.Grid()
	if grid == nil {
		return nil, errors.New("commit dialog: active tab has no grid")
	}
	if set == nil || !set.Identity().Compatible(grid.EditIdentity()) {
		return nil, models.ErrEditIdentityMismatch
	}
	return set.Identity().Names(), nil
}
