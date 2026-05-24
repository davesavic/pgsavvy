package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/gui"
	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/logs"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// connHelperAcquirer satisfies helpers.SessionAcquirer by re-resolving
// the live drivers.Connection from ConnectHelper at each call. The
// indirection matters because Connection is invalidated on Disconnect,
// so capturing a snapshot at wiring time would dangle.
type connHelperAcquirer struct{ h *data.ConnectHelper }

func (a connHelperAcquirer) AcquireSession(ctx context.Context) (drivers.Session, error) {
	if a.h == nil {
		return nil, errors.New("commit dialog: connect helper not wired")
	}
	conn := a.h.Connection()
	if conn == nil {
		return nil, errors.New("commit dialog: no active connection")
	}
	return conn.AcquireSession(ctx)
}

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
}

// commitApplyHook implements controllers.CommitDialogApplyHook.
type commitApplyHook struct{ deps commitDialogDeps }

func (h commitApplyHook) Apply(set *models.PendingEditSet, conn *models.Connection) error {
	pkCols, err := pkColsFromActiveTab(h.deps.tabs)
	if err != nil {
		return err
	}
	res, conflicts, err := h.deps.apply.Apply(context.Background(), set, pkCols, false)
	if err != nil {
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
	rows := len(res.RowsAffected)
	set.Clear()
	if h.deps.toast != nil {
		h.deps.toast.Show(fmt.Sprintf("%d row(s) committed", rows), 0)
	}
	return nil
}

// commitDryRunHook implements controllers.CommitDialogDryRunHook.
type commitDryRunHook struct{ deps commitDialogDeps }

func (h commitDryRunHook) DryRun(set *models.PendingEditSet, _ *models.Connection) ([]guicontext.DryRunStmtResult, error) {
	pkCols, err := pkColsFromActiveTab(h.deps.tabs)
	if err != nil {
		return nil, err
	}
	res, _, err := h.deps.apply.Apply(context.Background(), set, pkCols, true)
	if err != nil {
		return []guicontext.DryRunStmtResult{{Err: err}}, err
	}
	edits := set.Edits()
	out := make([]guicontext.DryRunStmtResult, 0, len(res.RowsAffected))
	for i, n := range res.RowsAffected {
		var col string
		if i < len(edits) {
			col = edits[i].Column
		}
		out = append(out, guicontext.DryRunStmtResult{
			SQL:          fmt.Sprintf("UPDATE %s.%s SET %s = $1 ...", set.Table.Schema, set.Table.Table, col),
			RowsAffected: int64(n),
		})
	}
	return out, nil
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
// commit dialog's onUI + conflictCtx fields. dbsavvy-lda (dbsavvy-8oo #7).
type conflictDialogDeps struct {
	apply         *helpers.CellApplyHelper
	tabs          *ui.ResultTabsHelper
	toast         *ui.ToastHelper
	activeSetFunc func() *models.PendingEditSet
}

// conflictRefreshHook implements controllers.ConflictDialogRefreshHook.
// Drops each conflicted edit from the active PendingEditSet and prompts
// the user to re-run their query for fresh data. The grid is NOT
// re-fetched in place — there is no grid UpdateRow API today (see
// dbsavvy-bb6 notes), so the toast guides the user instead.
type conflictRefreshHook struct{ deps conflictDialogDeps }

func (h conflictRefreshHook) Refresh(conflicts []models.ConflictedEdit, _ *models.Connection) error {
	if h.deps.activeSetFunc == nil {
		return errors.New("conflict dialog: pending edit set resolver not wired")
	}
	set := h.deps.activeSetFunc()
	if set == nil {
		return errors.New("conflict dialog: no active pending edit set")
	}
	for _, c := range conflicts {
		set.Remove(c.Edit.PrimaryKey, c.Edit.Column)
	}
	if h.deps.toast != nil {
		h.deps.toast.Show(
			fmt.Sprintf("%d conflicting edit(s) dropped — re-run query for fresh data", len(conflicts)),
			0,
		)
	}
	return nil
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
	pkCols, err := pkColsFromActiveTab(h.deps.tabs)
	if err != nil {
		return err
	}
	// Build an overwrite-only set containing just the conflicted edits.
	// Apply's transaction rolled back, so non-conflicting staged edits
	// are also un-applied; the conflict slice is the authoritative list
	// of what the user has chosen to force through, and the user will
	// re-run :w for the remaining non-conflicting edits.
	owSet := &models.PendingEditSet{Table: set.Table}
	for _, c := range conflicts {
		if addErr := owSet.Add(c.Edit); addErr != nil {
			return fmt.Errorf("conflict dialog: stage overwrite edit: %w", addErr)
		}
	}
	res, applyErr := h.deps.apply.Overwrite(context.Background(), owSet, pkCols)
	if applyErr != nil {
		return applyErr
	}
	// Drop the overwritten edits from the live staged set.
	for _, c := range conflicts {
		set.Remove(c.Edit.PrimaryKey, c.Edit.Column)
	}
	if h.deps.toast != nil {
		h.deps.toast.Show(
			fmt.Sprintf("%d row(s) overwritten — re-run query for fresh data", len(res.RowsAffected)),
			0,
		)
	}
	return nil
}

// pkColsFromActiveTab resolves the PK column names for the currently-
// active result tab. Returns an error when no tab is active, the tab
// has no grid, the grid has no row identity, or an identity index is
// out of range for the column list.
func pkColsFromActiveTab(tabs *ui.ResultTabsHelper) ([]string, error) {
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
	ri := grid.RowIdentity()
	if len(ri) == 0 {
		return nil, errors.New("commit dialog: active tab has no row identity")
	}
	cols := grid.Columns()
	out := make([]string, len(ri))
	for i, idx := range ri {
		if idx < 0 || idx >= len(cols) {
			return nil, fmt.Errorf("commit dialog: pk index %d out of range (%d cols)", idx, len(cols))
		}
		out[i] = cols[idx].Name
	}
	return out, nil
}
