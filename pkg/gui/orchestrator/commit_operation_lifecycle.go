package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/gui/grid"
	"github.com/davesavic/pgsavvy/pkg/logs"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// Independent of the UI queue and global workersWG: retirement can itself be a
// worker, and MainLoop may already have stopped before a receipt is produced.
type commitOperationOwner struct {
	mu         sync.Mutex
	retired    bool
	operations map[*commitOperation]struct{}
}

func (o *commitOperationOwner) cancel() {
	o.mu.Lock()
	defer o.mu.Unlock()
	for op := range o.operations {
		op.cancel()
	}
}

func (o *commitOperationOwner) retire() {
	o.mu.Lock()
	o.retired = true
	ops := make([]*commitOperation, 0, len(o.operations))
	for op := range o.operations {
		op.cancel()
		ops = append(ops, op)
	}
	o.mu.Unlock()
	for _, op := range ops {
		<-op.done
	}
}

func (g *Gui) cancelCommitOperations() {
	g.connectionLife.mu.Lock()
	defer g.connectionLife.mu.Unlock()
	if owner := g.connectionLife.owner; owner != nil {
		owner.commits.cancel()
	}
}

type commitOperation struct {
	ctx            context.Context
	cancel         context.CancelFunc
	done           chan struct{}
	live, snapshot *models.PendingEditSet
	owner          *connectionOwner
	generation     uint64
	pkCols         []string
	profile        models.Connection
	tab            *ui.Tab
	grid           *grid.View
	tabGeneration  uint64
	helper         *helpers.CellApplyHelper
}

type commitReceipt struct {
	result    helpers.ApplyResult
	conflicts []models.ConflictedEdit
	report    []guicontext.DryRunStmtResult
	err       error
}

type asyncCommitHooks struct {
	g         *Gui
	deps      commitDialogDeps
	activeSet func() *models.PendingEditSet
}

func (h asyncCommitHooks) capture(set *models.PendingEditSet, conn *models.Connection, selected []models.PendingEdit, overwrite bool) (*commitOperation, error) {
	if h.g == nil || conn == nil || set == nil || h.deps.tabs == nil {
		return nil, errors.New("commit dialog: operation not wired")
	}
	if conn.ReadOnly || (overwrite && conn.ConfirmWrites) {
		return nil, errors.New("commit dialog: writes not authorized")
	}
	currentProfile := h.g.connectionState.activeConnProfile
	if currentProfile == nil || currentProfile.Name != conn.Name || currentProfile.ReadOnly != conn.ReadOnly || currentProfile.ConfirmWrites != conn.ConfirmWrites {
		return nil, errors.New("commit dialog: confirmation policy changed; reopen the dialog")
	}
	if h.g.pendingEditReg == nil || h.g.pendingEditReg.Lookup(h.g.connectionState.activeConnID, set.Table) != set {
		return nil, errors.New("commit dialog: target changed; reopen the dialog")
	}
	tab := h.deps.tabs.Active()
	if tab == nil || tab.Grid() == nil || !tab.Grid().Editable() || tab.Grid().IdentityRef() != set.Table {
		return nil, errors.New("commit dialog: result changed; reopen the dialog")
	}
	pkCols, err := pkColsFromActiveTab(h.deps.tabs)
	if err != nil {
		return nil, err
	}
	snapshot, err := set.BeginCommit(selected)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	op := &commitOperation{
		ctx: ctx, cancel: cancel, done: make(chan struct{}), live: set, snapshot: snapshot,
		pkCols: pkCols, tab: tab, grid: tab.Grid(), tabGeneration: tab.EditabilityGeneration(),
		// No credential-bearing profile fields are needed by execution/publication.
		profile: models.Connection{Name: conn.Name, ReadOnly: conn.ReadOnly, ConfirmWrites: conn.ConfirmWrites, Color: conn.Color, Icon: conn.Icon, Label: conn.Label},
	}
	g := h.g
	g.connectionLife.mu.Lock()
	owner := g.connectionLife.owner
	if g.closed.Load() || owner == nil || !owner.published || owner.connection == nil || owner.profile != conn.Name || g.connectionState.activeConnID != conn.Name {
		g.connectionLife.mu.Unlock()
		cancel()
		set.SettleCommit(snapshot, false, false, nil, nil)
		return nil, errors.New("commit dialog: connection changed; reopen the dialog")
	}
	owner.commits.mu.Lock()
	if owner.commits.retired || !g.HoldBusy() {
		owner.commits.mu.Unlock()
		g.connectionLife.mu.Unlock()
		cancel()
		set.SettleCommit(snapshot, false, false, nil, nil)
		return nil, context.Canceled
	}
	op.owner, op.generation = owner, g.connectionState.connectGen.Load()
	op.helper = helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: owner.connection})
	if owner.commits.operations == nil {
		owner.commits.operations = make(map[*commitOperation]struct{})
	}
	owner.commits.operations[op] = struct{}{}
	owner.commits.mu.Unlock()
	g.connectionLife.mu.Unlock()
	return op, nil
}

// Only immutable operation inputs enter this function; no live UI/model reads.
func (op *commitOperation) execute(dryRun, overwrite bool) (receipt commitReceipt) {
	if overwrite {
		receipt.result, receipt.err = op.helper.Overwrite(op.ctx, op.snapshot, op.pkCols)
	} else {
		receipt.result, receipt.conflicts, receipt.err = op.helper.Apply(op.ctx, op.snapshot, op.pkCols, dryRun)
	}
	if dryRun && receipt.err == nil {
		batch, err := helpers.BuildUpdateBatch(op.snapshot.Table, op.pkCols, receipt.result.Edits, helpers.SQLModeLiteral, true)
		receipt.err = err
		for _, stmt := range batch {
			receipt.report = append(receipt.report, guicontext.DryRunStmtResult{SQL: stmt.SQL, RowsAffected: int64(receipt.result.RowsAffected[stmt.EditIndexes[0]])})
		}
	}
	return receipt
}

func (h asyncCommitHooks) launch(op *commitOperation, dryRun, overwrite bool, publish func(commitReceipt) error) {
	go func() {
		// A panic in a driver leaves the write outcome unproven. Fail closed;
		// never release a retryable expression merely because execution panicked.
		receipt := commitReceipt{}
		func() {
			defer func() {
				if recover() != nil {
					receipt.err = errors.New("commit operation failed unexpectedly; reconcile server state before retrying")
					receipt.result.CommitUnknown = !dryRun
				}
			}()
			receipt = op.execute(dryRun, overwrite)
		}()
		op.live.SettleCommit(op.snapshot, receipt.result.Committed, receipt.result.CommitUnknown, receipt.result.OriginalPKs, receipt.result.ResultPKs)
		op.cancel()
		if receipt.err != nil && h.deps.logger != nil {
			logs.Event(h.deps.logger, "warn", "commit_operation_failed", slog.Bool("committed", receipt.result.Committed), slog.Bool("unknown", receipt.result.CommitUnknown), slog.Bool("dry_run", dryRun))
		}
		h.g.ReleaseBusy()
		op.owner.commits.mu.Lock()
		delete(op.owner.commits.operations, op)
		close(op.done)
		op.owner.commits.mu.Unlock()
		// Mandatory settlement and session disposal precede optional publication.
		h.g.OnUIThread(func() error {
			if !h.current(op) {
				return nil
			}
			if err := publish(receipt); !errors.Is(err, context.Canceled) {
				return err
			}
			return nil
		})
	}()
}

func (h asyncCommitHooks) current(op *commitOperation) bool {
	g := h.g
	g.connectionLife.mu.Lock()
	current := !g.closed.Load() && g.connectionLife.owner == op.owner && g.connectionState.connectGen.Load() == op.generation
	g.connectionLife.mu.Unlock()
	if !current || h.deps.tabs.Active() != op.tab || op.tab.Grid() != op.grid || op.tab.EditabilityGeneration() != op.tabGeneration || op.grid.IdentityRef() != op.snapshot.Table {
		return false
	}
	for _, tab := range h.deps.tabs.Tabs() {
		if tab == op.tab {
			return true
		}
	}
	return false
}

func safeCommitError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, models.ErrCommitOutcomeUnknown) {
		return models.ErrCommitOutcomeUnknown
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	return errors.New("commit operation failed; pending edits retained")
}

func (h asyncCommitHooks) StartApply(set *models.PendingEditSet, conn *models.Connection, done func(error) error) (func(), error) {
	op, err := h.capture(set, conn, nil, false)
	if err != nil {
		return nil, err
	}
	h.launch(op, false, false, func(receipt commitReceipt) error {
		if receipt.result.Committed {
			h.publishCommitted(op, receipt, false)
			return done(nil)
		}
		if len(receipt.conflicts) != 0 {
			if h.deps.conflictCtx == nil || h.deps.tree == nil {
				return done(errors.New("commit dialog: conflict dialog not wired"))
			}
			if err := done(nil); err != nil {
				return err
			}
			if err := h.deps.conflictCtx.Open(receipt.conflicts, &op.profile); err != nil {
				return err
			}
			return h.deps.tree.Push(h.deps.conflictCtx)
		}
		err := safeCommitError(receipt.err)
		if receipt.result.CommitUnknown {
			err = models.ErrCommitOutcomeUnknown
		}
		if err != nil && h.deps.toast != nil {
			h.deps.toast.Show(err.Error(), 0)
		}
		return done(err)
	})
	return op.cancel, nil
}

func (h asyncCommitHooks) StartDryRun(set *models.PendingEditSet, conn *models.Connection, done func([]guicontext.DryRunStmtResult, error) error) (func(), error) {
	op, err := h.capture(set, conn, nil, false)
	if err != nil {
		return nil, err
	}
	h.launch(op, true, false, func(receipt commitReceipt) error {
		return done(receipt.report, safeCommitError(receipt.err))
	})
	return op.cancel, nil
}

func (h asyncCommitHooks) publishCommitted(op *commitOperation, receipt commitReceipt, overwrite bool) {
	if h.deps.evictCounts != nil {
		h.deps.evictCounts(op.snapshot.Table.Schema, op.snapshot.Table.Table)
	}
	op.grid.UpdateRowsFromOriginalPK(receipt.result.RefetchedColumns, receipt.result.RefetchedRows, receipt.result.RefetchedOriginalPKs)
	if receipt.err != nil {
		showCommitRefreshWarning(h.deps.toast, h.deps.logger)
		return
	}
	if h.deps.toast != nil {
		verb := "committed"
		if overwrite {
			verb = "overwritten"
		}
		h.deps.toast.Show(fmt.Sprintf("%d row(s) %s", len(receipt.result.ResultPKs), verb), 0)
	}
}

// Retains the synchronous controller interface: dispatch success dismisses the
// conflict popup; asynchronous failure is visible through the safe toast/event.
func (h asyncCommitHooks) Overwrite(conflicts []models.ConflictedEdit, conn *models.Connection) error {
	if h.activeSet == nil {
		return errors.New("conflict dialog: pending edit resolver not wired")
	}
	selected := make([]models.PendingEdit, 0, len(conflicts))
	for _, conflict := range conflicts {
		selected = append(selected, conflict.Edit)
	}
	op, err := h.capture(h.activeSet(), conn, selected, true)
	if err != nil {
		return err
	}
	h.launch(op, false, true, func(receipt commitReceipt) error {
		if receipt.result.Committed {
			h.publishCommitted(op, receipt, true)
			return nil
		}
		err := safeCommitError(receipt.err)
		if receipt.result.CommitUnknown {
			err = models.ErrCommitOutcomeUnknown
		}
		if err != nil && h.deps.toast != nil {
			h.deps.toast.Show(err.Error(), 0)
		}
		return nil
	})
	return nil
}
