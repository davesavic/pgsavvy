package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/query"
)

type asyncCommitDriver struct {
	types.GuiDriver
	queue chan func() error
}

func (d *asyncCommitDriver) Update(fn func() error)            { d.queue <- fn }
func (d *asyncCommitDriver) UpdateContentOnly(fn func() error) { d.queue <- fn }
func (*asyncCommitDriver) Close() error                        { return nil }

type asyncCommitConnection struct {
	drivers.Connection
	acquire func(context.Context) (drivers.Session, error)
	calls   atomic.Int32
}

func (c *asyncCommitConnection) AcquireSession(ctx context.Context) (drivers.Session, error) {
	c.calls.Add(1)
	return c.acquire(ctx)
}

func asyncCommitFixture(t *testing.T, conn drivers.Connection) (asyncCommitHooks, *models.PendingEditSet, *asyncCommitDriver) {
	t.Helper()
	tabs, _ := newConflictHookFixture(t, models.Row{Values: []any{int64(42), "loaded"}}, "id", "data")
	g := &Gui{pendingEditReg: newPendingEditRegistry()}
	g.spinnerState.workersWG = newWorkerGroup()
	g.connectionState.activeConnID = "A"
	g.connectionState.activeConnProfile = &models.Connection{Name: "A"}
	g.connectionLife.owner = &connectionOwner{profile: "A", connection: conn, published: true}
	d := &asyncCommitDriver{queue: make(chan func() error, 32)}
	g.driver = d
	set := g.pendingEditReg.For("A", models.Ref{Schema: "app", Table: "t"})
	if err := set.BindIdentity(tabs.Active().Grid().EditIdentity()); err != nil {
		t.Fatal(err)
	}
	if err := set.Add(models.PendingEdit{PrimaryKey: []any{int64(42)}, Column: "data", OldValue: "loaded", Kind: models.Expression, NewExpr: "data || 'x'"}); err != nil {
		t.Fatal(err)
	}
	h := asyncCommitHooks{g: g, deps: commitDialogDeps{tabs: tabs, toast: ui.NewToastHelper(nil)}, activeSet: func() *models.PendingEditSet { return set }}
	t.Cleanup(func() {
		g.cancelCommitOperations()
		if err := g.retireConnection(nil); err != nil {
			t.Error(err)
		}
	})
	return h, set, d
}

func asyncAwait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("controlled async barrier timed out")
	}
}

func asyncEnsureReleased(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

func asyncPublish(t *testing.T, d *asyncCommitDriver) {
	t.Helper()
	select {
	case fn := <-d.queue:
		if err := fn(); err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("completion was not scheduled")
	}
}

func TestReview_async_commit_bound_connection(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	defer asyncEnsureReleased(release)
	sess := &rowOutcomeSession{}
	a := &asyncCommitConnection{acquire: func(ctx context.Context) (drivers.Session, error) { close(entered); <-release; return sess, nil }}
	b := &asyncCommitConnection{acquire: func(context.Context) (drivers.Session, error) {
		t.Error("SQL operation reached B")
		return nil, errors.New("wrong target")
	}}
	h, set, d := asyncCommitFixture(t, a)
	op, err := h.capture(set, &models.Connection{Name: "A"}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	if _, err := h.capture(set, &models.Connection{Name: "A"}, nil, false); !errors.Is(err, models.ErrCommitInFlight) {
		t.Fatalf("duplicate: %v", err)
	}
	if err := set.Add(models.PendingEdit{PrimaryKey: []any{int64(42)}, Column: "other"}); !errors.Is(err, models.ErrCommitInFlight) {
		t.Fatalf("restaging: %v", err)
	}
	// The UI can change ownership while acquisition is blocked. The captured
	// acquirer remains A even if the current owner becomes B.
	h.g.connectionLife.mu.Lock()
	h.g.connectionLife.owner = &connectionOwner{profile: "B", connection: b, published: true}
	h.g.connectionLife.mu.Unlock()
	h.g.connectionState.activeConnID = "B"
	h.launch(op, false, false, func(commitReceipt) error { called = true; return nil })
	asyncAwait(t, entered)
	close(release)
	asyncAwait(t, op.done)
	asyncPublish(t, d)
	if a.calls.Load() != 1 || b.calls.Load() != 0 || !set.IsEmpty() || called || !sess.closed {
		t.Fatal("bound operation or stale publication invariant failed")
	}
	if len(sess.queries) == 0 || sess.queries[1].SQL == "" || !strings.Contains(sess.queries[1].SQL, `"app"."t"`) {
		t.Fatal("canonical target not executed")
	}
}

func TestReview_async_commit_settlement(t *testing.T) {
	for _, mode := range []string{"close_tab", "reuse_tab", "shutdown", "refresh_failure", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			committed, release := make(chan struct{}), make(chan struct{})
			defer asyncEnsureReleased(release)
			sess := &rowOutcomeSession{onCommit: func() { close(committed); <-release }}
			if mode == "refresh_failure" {
				sess.refreshErr = errors.New("synthetic-secret-do-not-log")
			}
			if mode == "unknown" {
				sess.commitErr = errors.New("synthetic-secret-do-not-log")
			}
			conn := &asyncCommitConnection{acquire: func(context.Context) (drivers.Session, error) { return sess, nil }}
			h, set, d := asyncCommitFixture(t, conn)
			var log bytes.Buffer
			h.deps.logger = slog.New(slog.NewJSONHandler(&log, nil))
			op, err := h.capture(set, &models.Connection{Name: "A"}, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			published := false
			h.launch(op, false, false, func(r commitReceipt) error { published = true; h.publishCommitted(op, r, false); return nil })
			asyncAwait(t, committed)
			switch mode {
			case "close_tab":
				h.deps.tabs.CloseAll()
			case "reuse_tab":
				op.tab.SetIdentity("A", query.ResultIdentity{})
				op.grid.SetEditability(true, []int{0}, "", set.Table)
			case "shutdown":
				h.g.closed.Store(true)
			}
			if err := set.Add(models.PendingEdit{PrimaryKey: []any{int64(42)}, Column: "data", NewExpr: "data || 'x'", Kind: models.Expression}); !errors.Is(err, models.ErrCommitInFlight) {
				t.Fatalf("restaged before settlement: %v", err)
			}
			close(release)
			asyncAwait(t, op.done)
			if mode != "shutdown" {
				asyncPublish(t, d)
			}
			if set.CommitInFlight() || !sess.closed {
				t.Fatal("mandatory settlement/disposal missing")
			}
			if mode == "unknown" {
				if !set.CommitOutcomeUnknown() || set.IsEmpty() {
					t.Fatal("unknown commit became replayable")
				}
				if _, err := set.BeginCommit(nil); !errors.Is(err, models.ErrCommitOutcomeUnknown) {
					t.Fatalf("unknown retry: %v", err)
				}
			} else if !set.IsEmpty() {
				t.Fatal("committed expression remains replayable")
			}
			if mode == "close_tab" || mode == "reuse_tab" || mode == "shutdown" {
				if published {
					t.Fatal("stale view published")
				}
			} else if !published {
				t.Fatal("live view failed to publish")
			}
			if mode == "refresh_failure" && !strings.Contains(h.deps.toast.Current(), "Writes committed") {
				t.Fatal("committed refresh failure hidden")
			}
			if strings.Contains(log.String(), "synthetic-secret-do-not-log") {
				t.Fatal("backend secret copied into telemetry")
			}
		})
	}
}

func TestReview_async_commit_readonly_viewer(t *testing.T) {
	for _, mode := range []string{"readonly", "refresh_invalidated"} {
		t.Run(mode, func(t *testing.T) {
			h, _, _ := asyncCommitFixture(t, &asyncCommitConnection{})
			g := h.deps.tabs.Active().Grid()
			g.SetCursor(0, 1)
			if mode == "readonly" {
				g.SetEditability(false, []int{0}, "read-only connection", g.IdentityRef())
			} else {
				g.SettleCommittedRows(g.CommitGeneration(), g.EditIdentity(), nil, nil, nil, false)
			}
			picker := cellEditorPicker{tabs: h.deps.tabs}
			ctx := guicontext.NewCellViewerContext(guicontext.NewBaseContext(guicontext.BaseContextOpts{Key: guicontext.CellViewerKey(), ViewName: "viewer", Kind: types.PERSISTENT_POPUP}), guicontext.Deps{})
			ctrl := controllers.NewCellViewerController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, ctx, &rowOutcomeFocus{}, picker)
			if err := ctrl.Open(commands.ExecCtx{}); err != nil {
				t.Fatal(err)
			}
			if !ctx.Active() {
				t.Fatal("noneditable grid cannot be viewed with production picker")
			}
			_, _, _, baseline, ok := g.CellEditSnapshot()
			if !ok || g.Editable() || !errors.Is(g.WithEditBaseline(baseline, func() error { t.Error("viewing authorized editing"); return nil }), models.ErrCommitSnapshotStale) {
				t.Fatal("read-only viewing weakened editing guard")
			}
		})
	}
}

type asyncReceiptSession struct {
	rowOutcomeSession
	value string
}

func (s *asyncReceiptSession) Execute(ctx context.Context, q models.Query) (models.Result, error) {
	result, err := s.rowOutcomeSession.Execute(ctx, q)
	if strings.HasPrefix(q.SQL, "SELECT *") && err == nil {
		result.Rows[0].Values[1] = s.value
	}
	return result, err
}

func TestReview_async_commit_reverse_receipts(t *testing.T) {
	first := &asyncReceiptSession{value: "first"}
	var session drivers.Session = first
	conn := &asyncCommitConnection{acquire: func(context.Context) (drivers.Session, error) { return session, nil }}
	h, set, d := asyncCommitFixture(t, conn)
	run := func() func() error {
		op, err := h.capture(set, &models.Connection{Name: "A"}, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		h.launch(op, false, false, func(r commitReceipt) error { h.publishCommitted(op, r, false); return nil })
		asyncAwait(t, op.done)
		select {
		case fn := <-d.queue:
			return fn
		case <-time.After(3 * time.Second):
			t.Fatal("receipt was not queued")
			return nil
		}
	}
	old := run()
	g := h.deps.tabs.Active().Grid()
	g.AppendRows([]models.Row{{Values: []any{int64(42), "reused original key"}}})
	if err := set.Add(models.PendingEdit{PrimaryKey: []any{int64(99)}, Column: "data", OldValue: "first", NewExpr: "data || 'next'", Kind: models.Expression}); err != nil {
		t.Fatal(err)
	}
	session = &asyncReceiptSession{value: "second"}
	newer := run()
	if err := newer(); err != nil {
		t.Fatal(err)
	}
	if err := old(); err != nil {
		t.Fatal(err)
	}
	rows := g.AllRows()
	if rows[0].Values[0] != int64(99) || rows[0].Values[1] != "second" || rows[1].Values[0] != int64(42) || rows[1].Values[1] != "reused original key" {
		t.Fatalf("out-of-order receipts reverted key/value baselines: %+v", rows)
	}
}

func TestReview_async_commit_stale_editor(t *testing.T) {
	for _, mode := range []string{"before_capture", "after_capture", "discard"} {
		t.Run(mode, func(t *testing.T) {
			h, set, _ := asyncCommitFixture(t, &asyncCommitConnection{})
			set.Clear()
			sample := &cellEditorSample{}
			picker := cellEditorPicker{tabs: h.deps.tabs, sample: sample}
			store := cellEditorStore{resolve: func() *models.PendingEditSet { return set }, sample: sample}
			_, _, pk, ok := picker.CellSnapshot()
			if !ok {
				t.Fatal("cell sampling failed")
			}
			captured := store.Capture()
			g := h.deps.tabs.Active().Grid()
			// Equal values still constitute a new authoritative receipt baseline.
			g.UpdateRowsFromOriginalPK(g.EditIdentity(), g.Columns(), []models.Row{{Values: []any{int64(42), "loaded"}}}, [][]any{pk})
			if mode == "before_capture" {
				captured = store.Capture()
			}
			edit := models.PendingEdit{PrimaryKey: pk, Column: "data", OldValue: "loaded", NewExpr: "data || 'next'", Kind: models.Expression}
			if mode == "discard" {
				if err := set.Add(edit); err != nil {
					t.Fatal(err)
				}
				if err := captured.Remove(pk, "data"); !errors.Is(err, models.ErrCommitSnapshotStale) {
					t.Fatalf("stale removal lost its failure: %v", err)
				}
				if !set.HasEdit(pk, "data") {
					t.Fatal("stale editor removed newer staged intent")
				}
			} else {
				if err := captured.Add(edit); !errors.Is(err, models.ErrCommitSnapshotStale) || !set.IsEmpty() {
					t.Fatalf("stale sampled original staged: err=%v count=%d", err, set.Count())
				}
			}
		})
	}
}

type asyncCellEditorFocus struct {
	onPop func()
	pops  int
}

func (*asyncCellEditorFocus) Push(types.IBaseContext) error { return nil }
func (f *asyncCellEditorFocus) Pop() error {
	f.pops++
	if f.onPop != nil {
		f.onPop()
	}
	return nil
}

func TestReview_async_commit_editor_recovery(t *testing.T) {
	for _, action := range []string{"commit", "discard_during_pop", "discard_before_pop"} {
		t.Run(action, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			defer asyncEnsureReleased(release)
			sess := &rowOutcomeSession{onCommit: func() { close(entered); <-release }}
			h, set, _ := asyncCommitFixture(t, &asyncCommitConnection{acquire: func(context.Context) (drivers.Session, error) { return sess, nil }})
			g := h.deps.tabs.Active().Grid()
			g.AppendRows([]models.Row{{Values: []any{int64(100), "other row"}}})
			g.SetCursor(1, 1)
			op, err := h.capture(set, &models.Connection{Name: "A"}, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			h.launch(op, false, false, func(commitReceipt) error { return nil })
			asyncAwait(t, entered)
			pk := []any{int64(100)}
			if action != "commit" {
				if err := set.Add(models.PendingEdit{PrimaryKey: pk, Column: "data", OldValue: "other row", NewValue: "staged", Kind: models.Literal}); err != nil {
					t.Fatal(err)
				}
			}
			sample := &cellEditorSample{}
			ctx := guicontext.NewCellEditorContext(guicontext.NewBaseContext(guicontext.BaseContextOpts{Key: guicontext.CellEditorKey(), ViewName: "editor", Kind: types.TEMPORARY_POPUP}), guicontext.Deps{})
			focus := &asyncCellEditorFocus{}
			ctrl := controllers.NewCellEditorController(nil, controllers.CoreDeps{}, controllers.UIDeps{Toast: h.deps.toast}, ctx, focus,
				cellEditorPicker{tabs: h.deps.tabs, sample: sample}, cellEditorStore{resolve: func() *models.PendingEditSet { return set }, sample: sample})
			if err := ctrl.Enter(commands.ExecCtx{}); err != nil || !ctx.Active() {
				t.Fatal("production cell editor did not open")
			}
			ctx.SetBuffer("recoverable draft")
			settle := func() { close(release); asyncAwait(t, op.done) }
			if action == "commit" {
				settle()
				err := ctrl.Commit(commands.ExecCtx{})
				if !errors.Is(err, models.ErrCommitSnapshotStale) || set.HasEdit(pk, "data") || !ctx.Active() || ctx.Buffer() != "recoverable draft" || focus.pops != 0 {
					t.Fatal("stale submission must reject staging without losing the draft or editor")
				}
			} else {
				if action == "discard_before_pop" {
					settle()
				} else {
					focus.onPop = settle
				}
				err := ctrl.Discard(commands.ExecCtx{})
				if !errors.Is(err, models.ErrCommitSnapshotStale) || !set.HasEdit(pk, "data") || ctx.Active() || focus.pops != 1 || strings.Contains(h.deps.toast.Current(), "discarded pending edit") {
					t.Fatal("stale discard must close the popup, retain pending intent and report failure without a success toast")
				}
			}
		})
	}
}

func TestReview_async_commit_delayed_baseline(t *testing.T) {
	for _, mode := range []string{"before_publication", "newer_values", "refresh_failure", "same_grid_reuse"} {
		t.Run(mode, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			defer asyncEnsureReleased(release)
			sess := &rowOutcomeSession{onCommit: func() { close(entered); <-release }}
			if mode == "refresh_failure" {
				sess.refreshErr = errors.New("synthetic refresh failure")
			}
			conn := &asyncCommitConnection{acquire: func(context.Context) (drivers.Session, error) { return sess, nil }}
			h, set, d := asyncCommitFixture(t, conn)
			op, err := h.capture(set, &models.Connection{Name: "A"}, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			h.launch(op, false, false, func(r commitReceipt) error { h.publishCommitted(op, r, false); return nil })
			asyncAwait(t, entered)
			if mode == "same_grid_reuse" {
				op.grid.SetColumns(op.grid.Columns())
				op.grid.SetEditability(true, []int{0}, "", set.Table)
				op.grid.AppendRows([]models.Row{{Values: []any{int64(42), "replacement"}}})
			}
			close(release)
			asyncAwait(t, op.done)
			if !set.IsEmpty() || set.CommitInFlight() || !sess.closed {
				t.Fatal("durable settlement depends on UI publication")
			}
			switch mode {
			case "before_publication":
				rows := op.grid.AllRows()
				if rows[0].Values[0] != int64(99) || rows[0].Values[1] != "server" {
					t.Fatal("reservations released before grid baseline refreshed")
				}
				sample := &cellEditorSample{}
				picker := cellEditorPicker{tabs: h.deps.tabs, sample: sample}
				op.grid.SetCursor(0, 1)
				value, _, pk, ok := picker.CellSnapshot()
				if !ok || value != "server" || pk[0] != int64(99) {
					t.Fatal("editor sampled stale original after settlement")
				}
				store := cellEditorStore{resolve: func() *models.PendingEditSet { return set }, sample: sample}
				if err := store.Capture().Add(models.PendingEdit{PrimaryKey: pk, Column: "data", OldValue: value, NewExpr: "data || 'next'", Kind: models.Expression}); err != nil {
					t.Fatal(err)
				}
			case "newer_values":
				rows := op.grid.AllRows()
				op.grid.UpdateRowsFromOriginalPK(op.grid.EditIdentity(), op.grid.Columns(), []models.Row{{Values: []any{rows[0].Values[0], "newer"}}}, [][]any{{rows[0].Values[0]}})
			case "refresh_failure":
				if op.grid.Editable() {
					t.Fatal("unproven committed baseline remains editable")
				}
			}
			asyncPublish(t, d)
			rows := op.grid.AllRows()
			if mode == "newer_values" && rows[0].Values[1] != "newer" {
				t.Fatal("delayed receipt overwrote newer grid values")
			}
			if mode == "same_grid_reuse" && rows[0].Values[1] != "replacement" {
				t.Fatal("receipt mutated replacement schema generation")
			}
		})
	}
}

func TestReview_async_commit_cancel_and_retire(t *testing.T) {
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer asyncEnsureReleased(release)
	conn := &asyncCommitConnection{acquire: func(ctx context.Context) (drivers.Session, error) {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-release
		return nil, ctx.Err()
	}}
	h, set, _ := asyncCommitFixture(t, conn)
	op, err := h.capture(set, &models.Connection{Name: "A"}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	h.launch(op, false, false, func(commitReceipt) error { return nil })
	asyncAwait(t, entered)
	retired := make(chan struct{})
	go func() { _ = h.g.retireConnection(op.owner); close(retired) }()
	asyncAwait(t, cancelled)
	select {
	case <-retired:
		t.Fatal("retirement did not wait for operation settlement")
	case <-time.After(30 * time.Millisecond):
	}
	if !set.CommitInFlight() {
		t.Fatal("retirement released rows before I/O settled")
	}
	close(release)
	asyncAwait(t, retired)
	if set.CommitInFlight() || set.IsEmpty() || set.CommitOutcomeUnknown() {
		t.Fatal("cancel-before-acquire lost pending intent or invented commit")
	}
}

func TestReview_async_commit_dryrun_and_overwrite(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "dry_run", true: "overwrite"}[overwrite], func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			defer asyncEnsureReleased(release)
			sess := &rowOutcomeSession{}
			conn := &asyncCommitConnection{acquire: func(context.Context) (drivers.Session, error) { close(entered); <-release; return sess, nil }}
			h, set, d := asyncCommitFixture(t, conn)
			var report []guicontext.DryRunStmtResult
			if overwrite {
				if err := h.Overwrite([]models.ConflictedEdit{{Edit: set.Edits()[0]}}, &models.Connection{Name: "A", ConfirmWrites: true}); err == nil {
					t.Fatal("overwrite confirmation restriction bypassed")
				}
				if err := h.Overwrite([]models.ConflictedEdit{{Edit: set.Edits()[0]}}, &models.Connection{Name: "A"}); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := h.StartDryRun(set, &models.Connection{Name: "A", ReadOnly: true}, func([]guicontext.DryRunStmtResult, error) error { return nil }); err == nil {
					t.Fatal("read-only restriction bypassed")
				}
				if _, err := h.StartDryRun(set, &models.Connection{Name: "A"}, func(r []guicontext.DryRunStmtResult, err error) error { report = r; return err }); err != nil {
					t.Fatal(err)
				}
			}
			asyncAwait(t, entered)
			owner := h.g.connectionLife.owner
			owner.commits.mu.Lock()
			var op *commitOperation
			for current := range owner.commits.operations {
				op = current
			}
			owner.commits.mu.Unlock()
			if op == nil {
				t.Fatal("operation not registered")
			}
			close(release)
			asyncAwait(t, op.done)
			asyncPublish(t, d)
			if overwrite {
				if !set.IsEmpty() || !strings.Contains(h.deps.toast.Current(), "overwritten") {
					t.Fatal("overwrite not settled/published")
				}
			} else if set.IsEmpty() || len(report) != 1 || report[0].RowsAffected != 1 || sess.queries[len(sess.queries)-1].SQL != "ROLLBACK" {
				t.Fatal("dry run changed pending state or lost report")
			}
		})
	}
}

func TestReview_async_commit_native_failure(t *testing.T) {
	for _, mode := range []string{"apply", "dry_run"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReview_async_commit_native_failure_process$", "-test.v")
			cmd.Env = append(os.Environ(), "PGSAVVY_ASYNC_FAILURE_PROBE="+mode)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("native failure %s: %v deadline=%v\n%s", mode, err, ctx.Err(), out)
			}
			t.Logf("%s", out)
		})
	}
}

func TestReview_async_commit_native_failure_process(t *testing.T) {
	mode := os.Getenv("PGSAVVY_ASYNC_FAILURE_PROBE")
	if mode == "" {
		return
	}
	native, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: 80, Height: 24})
	if err != nil {
		t.Fatal(err)
	}
	d := newGocuiDriver(native)
	defer func() { _ = d.Close() }()
	entered, release := make(chan struct{}), make(chan struct{})
	defer asyncEnsureReleased(release)
	conn := &asyncCommitConnection{acquire: func(context.Context) (drivers.Session, error) {
		close(entered)
		<-release
		return nil, errors.New("synthetic-secret-do-not-log")
	}}
	h, set, queued := asyncCommitFixture(t, conn)
	// Hold completion outside MainLoop until settlement, then enqueue the real
	// publication before input/quit. FIFO ordering makes post-failure input proof
	// independent of scheduler timing or op.done closing before publication.
	ctx := guicontext.NewCommitDialogContext(guicontext.NewBaseContext(guicontext.BaseContextOpts{Key: guicontext.CommitDialogKey(), ViewName: "commit", Kind: types.TEMPORARY_POPUP}), guicontext.Deps{})
	ctx.Open(set, &models.Connection{Name: "A"})
	ctrl := controllers.NewCommitDialogController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, controllers.EditDeps{}, ctx, &rowOutcomeFocus{})
	var log bytes.Buffer
	h.deps.logger = slog.New(slog.NewJSONHandler(&log, nil))
	ctrl.SetAsyncHook(h)
	store := keys.NewModeStore()
	store.Set(types.COMMAND_LINE, types.ModeCommand)
	matcher, err := keys.NewMatcher(keys.NewTrieSet(), keys.MatcherConfig{Modes: store})
	if err != nil {
		t.Fatal(err)
	}
	defer matcher.Cancel()
	dispatcher := NewMasterEditor(native, matcher, types.COMMAND_LINE).(Dispatcher)
	started, inputDelivered := false, false
	native.SetManagerFunc(func(n *gocui.Gui) error {
		v, err := n.SetView("input", 0, 0, 60, 10, 0)
		if err != nil && !errors.Is(err, gocui.ErrUnknownView) {
			return err
		}
		if started {
			return nil
		}
		started = true
		run := ctrl.Apply
		if mode == "dry_run" {
			run = ctrl.DryRun
		}
		if err := run(commands.ExecCtx{}); err != nil {
			return err
		}
		go func() {
			<-entered
			close(release)
			completion := <-queued.queue
			d.Update(completion)
			d.Update(func() error {
				if !ctx.Active() || set.IsEmpty() || set.CommitInFlight() || !ctx.ApplyEnabled() {
					return errors.New("failure lost pending edits or disabled retry")
				}
				if mode == "dry_run" {
					if len(ctx.DryRunResult()) != 1 || ctx.DryRunResult()[0].Err == nil || strings.Contains(ctx.DryRunResult()[0].Err.Error(), "synthetic-secret") {
						return errors.New("safe retryable dry-run report missing")
					}
				} else if !strings.Contains(h.deps.toast.Current(), "pending edits retained") {
					return errors.New("apply failure visibility missing")
				}
				if _, err := dispatcher.Dispatch(v, gocui.NewKeyRune('j')); err != nil {
					return err
				}
				inputDelivered = v.TextArea.GetContent() == "j"
				return gocui.ErrQuit
			})
		}()
		return nil
	})
	if err := d.MainLoop(); !errors.Is(err, gocui.ErrQuit) {
		t.Fatalf("ordinary database failure terminated MainLoop: %v", err)
	}
	if !inputDelivered || strings.Contains(log.String(), "synthetic-secret") {
		t.Fatal("post-failure input missing or secret logged")
	}
	t.Logf("native %s failure: pending edits and retry retained; safe failure visible; post-publication input rendered; explicit quit preserved", mode)
}

func TestReview_async_commit_native(t *testing.T) {
	for _, mode := range []string{"apply", "dry_run"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReview_async_commit_native_process$", "-test.v")
			cmd.Env = append(os.Environ(), "PGSAVVY_ASYNC_COMMIT_PROBE="+mode)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("native %s: %v deadline=%v\n%s", mode, err, ctx.Err(), out)
			}
			t.Logf("%s", out)
		})
	}
}

func TestReview_async_commit_native_process(t *testing.T) {
	mode := os.Getenv("PGSAVVY_ASYNC_COMMIT_PROBE")
	if mode == "" {
		return
	}
	native, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: 80, Height: 24})
	if err != nil {
		t.Fatal(err)
	}
	d := newGocuiDriver(native)
	defer func() { _ = d.Close() }()
	entered, release := make(chan struct{}), make(chan struct{})
	defer asyncEnsureReleased(release)
	sess := &rowOutcomeSession{}
	conn := &asyncCommitConnection{acquire: func(context.Context) (drivers.Session, error) { close(entered); <-release; return sess, nil }}
	h, set, _ := asyncCommitFixture(t, conn)
	h.g.driver = d
	ctx := guicontext.NewCommitDialogContext(guicontext.NewBaseContext(guicontext.BaseContextOpts{Key: guicontext.CommitDialogKey(), ViewName: "commit", Kind: types.TEMPORARY_POPUP}), guicontext.Deps{})
	ctx.Open(set, &models.Connection{Name: "A"})
	ctrl := controllers.NewCommitDialogController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, controllers.EditDeps{}, ctx, &rowOutcomeFocus{})
	ctrl.SetAsyncHook(h)
	store := keys.NewModeStore()
	store.Set(types.COMMAND_LINE, types.ModeCommand)
	matcher, err := keys.NewMatcher(keys.NewTrieSet(), keys.MatcherConfig{Modes: store})
	if err != nil {
		t.Fatal(err)
	}
	defer matcher.Cancel()
	dispatcher := NewMasterEditor(native, matcher, types.COMMAND_LINE).(Dispatcher)
	inputDelivered := false
	started := false
	native.SetManagerFunc(func(n *gocui.Gui) error {
		v, err := n.SetView("input", 0, 0, 60, 10, 0)
		if err != nil && !errors.Is(err, gocui.ErrUnknownView) {
			return err
		}
		if started {
			return nil
		}
		started = true
		go func() {
			<-entered
			d.Update(func() error {
				if _, err := dispatcher.Dispatch(v, gocui.NewKeyRune('j')); err != nil {
					return err
				}
				if v.TextArea.GetContent() != "j" {
					return errors.New("native input not delivered while acquiring")
				}
				inputDelivered = true
				close(release)
				return nil
			})
		}()
		run := ctrl.Apply
		if mode == "dry_run" {
			run = ctrl.DryRun
		}
		if err := run(commands.ExecCtx{}); err != nil {
			return err
		}
		h.g.connectionLife.owner.commits.mu.Lock()
		var op *commitOperation
		for current := range h.g.connectionLife.owner.commits.operations {
			op = current
		}
		h.g.connectionLife.owner.commits.mu.Unlock()
		if op == nil {
			return errors.New("native operation not registered")
		}
		go func() { <-op.done; d.Update(func() error { return gocui.ErrQuit }) }()
		return nil
	})
	if err := d.MainLoop(); !errors.Is(err, gocui.ErrQuit) {
		t.Fatal(err)
	}
	if !inputDelivered || set.CommitInFlight() || !sess.closed {
		t.Fatal("native UI blocked or operation unsettled")
	}
	if mode == "apply" && !set.IsEmpty() {
		t.Fatal("native apply remains replayable")
	}
	if mode == "dry_run" && set.IsEmpty() {
		t.Fatal("native dry-run consumed pending edits")
	}
	t.Logf("native %s: input j rendered during blocked acquisition; worker settled; MainLoop quit", mode)
}

type asyncLiveSession struct {
	drivers.Session
	updateEntered chan struct{}
	committed     chan struct{}
	release       chan struct{}
	unknown       bool
}

func (s *asyncLiveSession) Execute(ctx context.Context, q models.Query) (models.Result, error) {
	if strings.HasPrefix(q.SQL, "UPDATE ") && s.updateEntered != nil {
		close(s.updateEntered)
	}
	res, err := s.Session.Execute(ctx, q)
	if err == nil && q.SQL == "COMMIT" && s.committed != nil {
		close(s.committed)
		<-s.release
		if s.unknown {
			return res, errors.New("synthetic lost COMMIT acknowledgement")
		}
	}
	return res, err
}

func TestReview_async_commit_live(t *testing.T) {
	dsn := os.Getenv("PGSAVVY_TEST_PG")
	if dsn == "" {
		t.Skip("PGSAVVY_TEST_PG unset; isolated fixture required")
	}
	for _, mode := range []string{"switch_A_B", "lock_wait", "shutdown_after_commit", "unknown_after_commit"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			factory := pg.New(nil)
			drv, err := factory(ctx)
			if err != nil {
				t.Fatal(err)
			}
			root, err := drv.Open(ctx, models.Connection{Name: "root", Driver: "postgres", DSN: dsn}, nil)
			if err != nil {
				t.Fatal("owned fixture root connection failed")
			}
			t.Cleanup(func() { _ = root.Close() })
			admin, err := root.AcquireSession(ctx)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = admin.Close() })
			var pools []drivers.Connection
			var sessions []drivers.Session
			for _, label := range []string{"a", "b"} {
				name := fmt.Sprintf("review_async_%s_%d", label, time.Now().UnixNano())
				if _, err := admin.Execute(ctx, models.Query{SQL: "CREATE DATABASE " + pg.QuoteIdent(name)}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cleanupCancel()
					if _, err := admin.Execute(cleanupCtx, models.Query{SQL: "DROP DATABASE " + pg.QuoteIdent(name)}); err != nil {
						t.Errorf("owned test database cleanup failed: %v", err)
					}
				})
				u, err := url.Parse(dsn)
				if err != nil {
					t.Fatal("fixture URL invalid")
				}
				u.Path = "/" + name
				pool, err := drv.Open(ctx, models.Connection{Name: label, Driver: "postgres", DSN: u.String()}, nil)
				if err != nil {
					t.Fatal("owned fixture database open failed")
				}
				t.Cleanup(func() { _ = pool.Close() })
				sess, err := pool.AcquireSession(ctx)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = sess.Close() })
				for _, sql := range []string{"CREATE TABLE public.items (id integer PRIMARY KEY,data integer)", "INSERT INTO public.items VALUES (42,10),(7,20)"} {
					if _, err := sess.Execute(ctx, models.Query{SQL: sql}); err != nil {
						t.Fatal(err)
					}
				}
				pools = append(pools, pool)
				sessions = append(sessions, sess)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			defer asyncEnsureReleased(release)
			updateEntered, committed := make(chan struct{}), make(chan struct{})
			a := &asyncCommitConnection{acquire: func(acquireCtx context.Context) (drivers.Session, error) {
				if mode == "switch_A_B" {
					close(entered)
					<-release
				}
				s, err := pools[0].AcquireSession(acquireCtx)
				if err != nil {
					return nil, err
				}
				wrapper := &asyncLiveSession{Session: s}
				if mode == "lock_wait" {
					wrapper.updateEntered = updateEntered
				}
				if mode == "shutdown_after_commit" || mode == "unknown_after_commit" {
					wrapper.committed = committed
					wrapper.release = release
					wrapper.unknown = mode == "unknown_after_commit"
				}
				return wrapper, nil
			}}
			h, _, d := asyncCommitFixture(t, a)
			set := h.g.pendingEditReg.For("A", models.Ref{Schema: "public", Table: "items"})
			stream, err := sessions[0].Stream(ctx, models.Query{SQL: "SELECT id,data FROM public.items"})
			if err != nil {
				t.Fatal(err)
			}
			cols := stream.Columns()
			if err := stream.Close(); err != nil {
				t.Fatal(err)
			}
			ref, rowID, reason, resolved, err := pg.ResolveEditability(ctx, sessions[0].(*pg.Session), cols)
			if err != nil || reason != "" || ref != set.Table {
				t.Fatalf("live target metadata unresolved: %v %q", err, reason)
			}
			gv := h.deps.tabs.Active().Grid()
			gv.SetColumns(resolved)
			gv.SetEditability(true, rowID, "", ref)
			if err := set.BindIdentity(gv.EditIdentity()); err != nil {
				t.Fatal(err)
			}
			if err := set.Add(models.PendingEdit{PrimaryKey: []any{int32(42)}, Column: "data", OldValue: int32(10), Kind: models.Expression, NewExpr: "data + 1"}); err != nil {
				t.Fatal(err)
			}
			h.deps.tabs.Active().Grid().AppendRows([]models.Row{{Values: []any{int32(42), int32(10)}}})
			if mode == "lock_wait" {
				for _, sql := range []string{"BEGIN", "UPDATE public.items SET data=data WHERE id=42"} {
					if _, err := sessions[0].Execute(ctx, models.Query{SQL: sql}); err != nil {
						t.Fatal(err)
					}
				}
			}
			op, err := h.capture(set, &models.Connection{Name: "A"}, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			published := false
			if mode == "switch_A_B" {
				h.g.connectionLife.mu.Lock()
				h.g.connectionLife.owner = &connectionOwner{profile: "B", connection: pools[1], published: true}
				h.g.connectionLife.mu.Unlock()
				h.g.connectionState.activeConnID = "B"
			}
			h.launch(op, false, false, func(r commitReceipt) error { published = true; return nil })
			var closing chan error
			switch mode {
			case "switch_A_B":
				asyncAwait(t, entered)
				close(release)
			case "lock_wait":
				asyncAwait(t, updateEntered)
				select {
				case <-op.done:
					t.Fatal("lock contention did not block worker")
				default:
				}
				input := false
				h.g.OnUIThread(func() error { input = true; return nil })
				asyncPublish(t, d)
				if !input {
					t.Fatal("input publication blocked by database lock")
				}
				if _, err := sessions[0].Execute(ctx, models.Query{SQL: "ROLLBACK"}); err != nil {
					t.Fatal(err)
				}
			case "shutdown_after_commit":
				asyncAwait(t, committed)
				closing = make(chan error, 1)
				go func() { closing <- h.g.Close() }()
				asyncAwait(t, op.ctx.Done())
				close(release)
			case "unknown_after_commit":
				asyncAwait(t, committed)
				close(release)
			}
			asyncAwait(t, op.done)
			if closing != nil {
				if err := <-closing; err != nil {
					t.Fatal(err)
				}
			} else {
				asyncPublish(t, d)
			}
			if mode == "unknown_after_commit" {
				if _, err := set.BeginCommit(nil); !errors.Is(err, models.ErrCommitOutcomeUnknown) {
					t.Fatalf("unknown expression retry: %v", err)
				}
			} else if !set.IsEmpty() {
				t.Fatal("confirmed expression remains replayable")
			}
			for i, sess := range sessions {
				res, err := sess.Execute(ctx, models.Query{SQL: "SELECT id,data FROM public.items ORDER BY id"})
				if err != nil {
					t.Fatal(err)
				}
				want := int32(10)
				if i == 0 {
					want = 11
				}
				if len(res.Rows) != 2 || res.Rows[0].Values[1] != int32(20) || res.Rows[1].Values[1] != want {
					t.Fatalf("database %d changed wrong target/rows: %+v", i, res.Rows)
				}
			}
			if mode == "unknown_after_commit" {
				if err := set.ReconcileCommitOutcome(true); err != nil {
					t.Fatal(err)
				}
				if !set.IsEmpty() {
					t.Fatal("explicit server-checked receipt not retired")
				}
			}
			if (mode == "switch_A_B" || mode == "shutdown_after_commit") && published {
				t.Fatal("stale/shutdown visual publication ran")
			}
			t.Logf("live %s: A expression applied once; B and unrelated A row unchanged; mandatory receipt settled", mode)
		})
	}
}
