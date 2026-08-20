package controllers_test

import (
	stdcontext "context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/logs"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
)

// C2 async-launch controller tests: with a UI scheduler wired
// (ThreadingDeps.OnUIThread), runStatement / reRunActiveTab enqueue onto
// the QueryRunner launch queue and return; the post-launch continuation
// (surfaceErr, Notice.AttachStream, openResultTab, reattach) arrives on
// the UI thread via the pump below. Without a scheduler the synchronous
// path is used (covered by the existing query editor tests).

// uiPump is a mutex-guarded UI-thread stand-in: OnUIThread posts; the
// test drains. Running the posted closures on the test goroutine gives
// the assertions a happens-before edge over the launcher goroutine.
type uiPump struct {
	mu  sync.Mutex
	fns []func() error
}

func (p *uiPump) post(fn func() error) {
	p.mu.Lock()
	p.fns = append(p.fns, fn)
	p.mu.Unlock()
}

// drain runs every queued fn and returns how many ran.
func (p *uiPump) drain() int {
	p.mu.Lock()
	fns := p.fns
	p.fns = nil
	p.mu.Unlock()
	for _, fn := range fns {
		_ = fn()
	}
	return len(fns)
}

// pumpUntil drains until cond holds or the timeout elapses.
func (p *uiPump) pumpUntil(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		if cond() {
			return
		}
		p.drain()
		if cond() {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("pump: %s never became true within %v", what, timeout)
		case <-time.After(time.Millisecond):
		}
	}
}

// launchInner is a thread-safe drivers.Session fake for the async path:
// records arrive on the launcher goroutine; staged Stream calls may park
// on a gate (honouring the launch ctx) or resolve immediately; streamErr
// replays for every Stream. A real session.SQLSession wraps it so the
// controller sees real RunHandles (tabs open).
type launchInner struct {
	mu        sync.Mutex
	events    []string
	staged    []launchStaged
	streamErr error

	// explainErr / explainPlan replay for every Explain call — the async
	// EXPLAIN/ANALYZE controller tests inject an error or a canned plan
	// through these. explainGate, when non-nil, parks Explain until it is
	// closed (or the op ctx aborts) so a test can hold the EXPLAIN op
	// mid-flight on the launcher.
	explainErr  error
	explainPlan models.Plan
	explainGate chan struct{}
}

type launchStaged struct {
	gate chan struct{}
}

func (l *launchInner) record(ev string) {
	l.mu.Lock()
	l.events = append(l.events, ev)
	l.mu.Unlock()
}

func (l *launchInner) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.events))
	copy(out, l.events)
	return out
}

func (l *launchInner) stage(gate chan struct{}) {
	l.mu.Lock()
	l.staged = append(l.staged, launchStaged{gate: gate})
	l.mu.Unlock()
}

func (l *launchInner) Execute(_ stdcontext.Context, q models.Query) (models.Result, error) {
	l.record("exec:" + q.SQL)
	return models.Result{}, nil
}

func (l *launchInner) Stream(ctx stdcontext.Context, q models.Query) (drivers.RowStream, error) {
	l.mu.Lock()
	err := l.streamErr
	var gate chan struct{}
	if len(l.staged) > 0 {
		gate = l.staged[0].gate
		l.staged = l.staged[1:]
	}
	l.mu.Unlock()

	l.record("stream:" + q.SQL + ":start")
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			l.record("stream:" + q.SQL + ":ctx-aborted")
			return nil, ctx.Err()
		}
	}
	l.record("stream:" + q.SQL + ":end")
	if err != nil {
		return nil, err
	}
	return &eofRowStream{}, nil
}

// eofRowStream serves an immediate clean EOF.
type eofRowStream struct{}

func (e *eofRowStream) Columns() []models.ColumnMeta { return nil }
func (e *eofRowStream) QueryID() models.QueryID      { return models.QueryID{} }
func (e *eofRowStream) RowsAffected() int64          { return 0 }
func (e *eofRowStream) Close() error                 { return nil }
func (e *eofRowStream) Next(stdcontext.Context) (models.Row, bool, error) {
	return models.Row{}, false, nil
}

// launchConn is the inert drivers.Connection session.New requires.
type launchConn struct{}

func (launchConn) Close() error                  { return nil }
func (launchConn) Ping(stdcontext.Context) error { return nil }
func (launchConn) ServerVersion() string         { return "fake" }
func (launchConn) AcquireSession(stdcontext.Context) (drivers.Session, error) {
	return nil, nil
}
func (launchConn) Cancel(stdcontext.Context, models.QueryID) error { return nil }

// The remaining drivers.Session surface is inert for these tests.
func (l *launchInner) Close() error                                                { return nil }
func (l *launchInner) ID() models.SessionID                                        { return 9 }
func (l *launchInner) ListDatabases(stdcontext.Context) ([]models.Database, error) { return nil, nil }
func (l *launchInner) ListSchemas(stdcontext.Context, string) ([]models.Schema, error) {
	return nil, nil
}

func (l *launchInner) ListTables(stdcontext.Context, string) ([]*models.Table, error) {
	return nil, nil
}

func (l *launchInner) ListColumns(stdcontext.Context, string, string) ([]models.Column, error) {
	return nil, nil
}

func (l *launchInner) ListIndexes(stdcontext.Context, string, string) ([]models.Index, error) {
	return nil, nil
}

func (l *launchInner) ListConstraints(stdcontext.Context, string, string) ([]models.Constraint, error) {
	return nil, nil
}

func (l *launchInner) ListForeignKeys(stdcontext.Context, string, string) ([]models.ForeignKey, error) {
	return nil, nil
}

func (l *launchInner) ListInboundForeignKeys(stdcontext.Context, string, string) ([]models.ForeignKey, error) {
	return nil, nil
}

func (l *launchInner) TableStats(stdcontext.Context, string, string) (int64, int64, error) {
	return 0, 0, nil
}
func (l *launchInner) ListFunctions(stdcontext.Context) ([]string, error) { return nil, nil }
func (l *launchInner) DescribeFunction(stdcontext.Context, string, string) ([]models.FunctionDetail, error) {
	return nil, nil
}

func (l *launchInner) Explain(ctx stdcontext.Context, _ models.Query, _ bool) (models.Plan, error) {
	if l.explainGate != nil {
		<-l.explainGate
	}
	return l.explainPlan, l.explainErr
}

func (l *launchInner) Begin(stdcontext.Context, models.TxOptions) (drivers.Transaction, error) {
	l.record("begin")
	return launchTx{}, nil
}

// launchTx is the minimal drivers.Transaction the async analyze-wrap path
// rolls back after EXPLAIN.
type launchTx struct{}

func (launchTx) Commit(stdcontext.Context) error                 { return nil }
func (launchTx) Rollback(stdcontext.Context) error               { return nil }
func (launchTx) Savepoint(stdcontext.Context, string) error      { return nil }
func (launchTx) Release(stdcontext.Context, string) error        { return nil }
func (launchTx) RollbackTo(stdcontext.Context, string) error     { return nil }
func (launchTx) Savepoints() []string                            { return nil }
func (launchTx) Status() models.TxStatus                         { return models.TxActive }
func (launchTx) ObserveError(error)                              {}
func (launchTx) StatementCount() int                             { return 0 }
func (l *launchInner) InTransaction() bool                       { return false }
func (l *launchInner) CurrentTransaction() drivers.Transaction   { return nil }
func (l *launchInner) LiveTxStatus() (models.TxStatus, []string) { return "", nil }
func (l *launchInner) Encoder() drivers.Encoder                  { return launchEncoder{} }

type launchEncoder struct{}

func (launchEncoder) EncodeLiteral(any, uint32) string { return "NULL" }

// fakeNoticeReporter records the NoticeReporter call sequence.
type fakeNoticeReporter struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeNoticeReporter) OnRunStart(runID string) {
	f.mu.Lock()
	f.calls = append(f.calls, "start:"+runID)
	f.mu.Unlock()
}

func (f *fakeNoticeReporter) OnRunEnd(runID string) {
	f.mu.Lock()
	f.calls = append(f.calls, "end:"+runID)
	f.mu.Unlock()
}

func (f *fakeNoticeReporter) OnNotice(pgconn.Notice) {}

func (f *fakeNoticeReporter) AttachStream(_ *session.RunHandle) {
	f.mu.Lock()
	f.calls = append(f.calls, "attach")
	f.mu.Unlock()
}

func (f *fakeNoticeReporter) Finish(runID string) {
	f.mu.Lock()
	f.calls = append(f.calls, "finish:"+runID)
	f.mu.Unlock()
}

func (f *fakeNoticeReporter) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

// reattachTabs extends fakeResultTabs with the sort + reattach surfaces
// (ResultTabSorter / ResultTabSortHooker / ResultTabReattacher).
type reattachTabs struct {
	fakeResultTabs
	mu         sync.Mutex
	reattached []reattachCall
	sortHook   func(col int)
}

type reattachCall struct {
	RunSQL, OrigSQL string
}

func (r *reattachTabs) ActiveTabOrigin() (string, []any, string) {
	return "SELECT orig", nil, ""
}

func (r *reattachTabs) ReattachActiveTab(_ *session.RunHandle, runSQL, origSQL string) {
	r.mu.Lock()
	r.reattached = append(r.reattached, reattachCall{runSQL, origSQL})
	r.mu.Unlock()
}

func (r *reattachTabs) SortActiveTab(col int) (string, bool, string) {
	return "SELECT orig ORDER BY 1", true, ""
}

func (r *reattachTabs) SetOnSortRequest(fn func(col int)) {
	r.mu.Lock()
	r.sortHook = fn
	r.mu.Unlock()
}

// asyncBag wires a query-editor controller over the pump + a real
// SQLSession wrapping the launchInner fake (real RunHandles → tabs).
type asyncBag struct {
	pump   *uiPump
	sess   *launchInner
	runner *data.QueryRunner
	tabs   *drainingTabs
	toast  *fakeToast
	notice *fakeNoticeReporter
}

func newAsyncBag(sess *launchInner) *asyncBag {
	sqlSess := session.New(&launchConn{}, sess, session.Options{})
	return &asyncBag{
		pump:   &uiPump{},
		sess:   sess,
		runner: data.NewQueryRunnerForSession(sqlSess, drivers.Capabilities{HasLiveCancel: true}),
		tabs:   &drainingTabs{},
		toast:  &fakeToast{},
		notice: &fakeNoticeReporter{},
	}
}

// drainingTabs extends fakeResultTabs with the tab-owns-drain contract:
// opening a result tab starts draining the RunHandle's rows (the RBM
// stand-in), so the real SQLSession's streamMu releases after the
// initial fill — without it a real session deadlocks consecutive
// undrained streams.
type drainingTabs struct {
	fakeResultTabs
}

func (d *drainingTabs) OpenResultTab(label string, rh *session.RunHandle) error {
	err := d.fakeResultTabs.OpenResultTab(label, rh)
	if rh != nil {
		go func() {
			for {
				if _, ok, _ := rh.Rows().Next(stdcontext.Background()); !ok {
					return
				}
			}
		}()
	}
	return err
}

func (b *asyncBag) controller(t *fakeEditorBuffer) *controllers.QueryEditorController {
	base := newBag() // fakes for Confirm etc. (OnUIThread nil — overridden below)
	base.HelperBag.QueryRunner = b.runner
	base.HelperBag.EditorBuffer = t
	base.HelperBag.ResultTabs = b.tabs
	base.HelperBag.Toast = b.toast
	base.HelperBag.Notice = b.notice
	base.HelperBag.ThreadingDeps = controllers.ThreadingDeps{
		OnUIThread:            b.pump.post,
		OnUIThreadContentOnly: b.pump.post,
		OnWorker: func(fn func(gocui.Task) error) {
			_ = fn(nil)
		},
	}
	return controllers.NewQueryEditorController(nil, base.HelperBag.CoreDeps, base.HelperBag.NavDeps, base.HelperBag.UIDeps, base.HelperBag.QueryDeps, base.HelperBag.ThreadingDeps)
}

// TestRunStatementAsyncReturnsAfterEnqueue is AC1 at the controller
// seam: with a UI scheduler wired, the <leader>r handler returns after
// enqueue (under the 50ms budget) while the session Stream is still
// parked; the tab + notice continuation then arrives on the UI thread.
func TestRunStatementAsyncReturnsAfterEnqueue(t *testing.T) {
	sess := &launchInner{}
	gate := make(chan struct{})
	sess.stage(gate)
	b := newAsyncBag(sess)
	ctrl := b.controller(&fakeEditorBuffer{Text: "SELECT 1;", Off: 3})
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	cmd, _ := reg.Get(commands.QueryRun)

	start := time.Now()
	handlerDone := make(chan error, 1)
	go func() { handlerDone <- cmd.Handler(commands.ExecCtx{}) }()
	select {
	case err := <-handlerDone:
		if err != nil {
			t.Fatalf("Run handler err = %v", err)
		}
		if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
			t.Fatalf("handler took %vms — want enqueue-only, <50ms", elapsed.Milliseconds())
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("handler still blocked after 100ms while the stream is parked — the launch is synchronous")
	}
	// The launch is parked inside Stream; no tab may exist yet (the
	// continuation arrives on the UI thread, which we hold).
	if len(b.tabs.resultCalls) != 0 {
		t.Fatalf("tab opened before the stream resolved: %#v", b.tabs.resultCalls)
	}

	close(gate)
	b.pump.pumpUntil(t, 2*time.Second, "one result tab", func() bool {
		return len(b.tabs.resultCalls) == 1
	})
	b.pump.pumpUntil(t, 2*time.Second, "notice finish", func() bool {
		for _, c := range b.notice.snapshot() {
			if strings.HasPrefix(c, "finish:") {
				return true
			}
		}
		return false
	})
	// start, attach, finish — Attach between them, Finish after the tab.
	calls := b.notice.snapshot()
	if len(calls) != 3 || !strings.HasPrefix(calls[0], "start:") || calls[1] != "attach" || !strings.HasPrefix(calls[2], "finish:") {
		t.Fatalf("notice calls = %v, want start, attach, finish", calls)
	}
}

// TestRunAllAsyncFanOutAggregates is AC4 for handleRunAll: N statements
// open N tabs in order on the UI thread, and the Notice run scope
// Finish fires exactly once, after every statement's ack.
func TestRunAllAsyncFanOutAggregates(t *testing.T) {
	sess := &launchInner{}
	b := newAsyncBag(sess)
	ctrl := b.controller(&fakeEditorBuffer{Text: "SELECT 1; SELECT 2; SELECT 3;"})
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	cmd, _ := reg.Get(commands.QueryRunAll)
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("RunAll handler err = %v", err)
	}

	b.pump.pumpUntil(t, 2*time.Second, "three tabs", func() bool {
		return len(b.tabs.resultCalls) == 3
	})
	calls := b.notice.snapshot()
	// start, attach×3 (interleaved with nothing else), finish — Finish
	// must be the LAST call for the run's ID.
	if len(calls) != 5 {
		t.Fatalf("notice calls = %v, want start + 3 attaches + finish", calls)
	}
	if !strings.HasPrefix(calls[0], "start:") || !strings.HasPrefix(calls[4], "finish:") {
		t.Fatalf("notice calls = %v, want Finish last", calls)
	}
	for i := 1; i <= 3; i++ {
		if calls[i] != "attach" {
			t.Fatalf("notice calls = %v, want attaches in the middle", calls)
		}
	}
	for i, want := range []string{"SELECT 1", "SELECT 2", "SELECT 3"} {
		if b.tabs.resultCalls[i].Label != want {
			t.Fatalf("tab[%d].Label = %q, want %q (FIFO order)", i, b.tabs.resultCalls[i].Label, want)
		}
	}
}

// TestRunOneAsyncPreemptPendingToastExactlyOnce mirrors the synchronous
// witness through the queue: an ErrPreemptPending launch surfaces as
// exactly one transient toast (no error tab), marshalled to the UI
// thread.
func TestRunOneAsyncPreemptPendingToastExactlyOnce(t *testing.T) {
	sess := &launchInner{streamErr: session.ErrPreemptPending}
	b := newAsyncBag(sess)
	ctrl := b.controller(&fakeEditorBuffer{Text: "SELECT 1;", Off: 3})
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	cmd, _ := reg.Get(commands.QueryRun)
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Run handler err = %v", err)
	}

	b.pump.pumpUntil(t, 2*time.Second, "one toast", func() bool {
		return len(b.toast.msgs) == 1
	})
	if len(b.tabs.errorCalls) != 0 {
		t.Fatalf("ErrPreemptPending opened %d error tabs, want 0", len(b.tabs.errorCalls))
	}
	calls := b.notice.snapshot()
	if len(calls) != 2 || !strings.HasPrefix(calls[0], "start:") || !strings.HasPrefix(calls[1], "end:") {
		t.Fatalf("notice calls = %v, want start + end (nothing attached)", calls)
	}
}

// TestRunOneAsyncErrNoSessionSurfacesMarshalled covers the edge path: a
// launch that loses its session surfaces ErrNoSession via the
// marshalled surfaceErr (error tab), not on the worker thread.
func TestRunOneAsyncErrNoSessionSurfacesMarshalled(t *testing.T) {
	sess := &launchInner{streamErr: data.ErrNoSession}
	b := newAsyncBag(sess)
	ctrl := b.controller(&fakeEditorBuffer{Text: "SELECT 1;", Off: 3})
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	cmd, _ := reg.Get(commands.QueryRun)
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Run handler err = %v", err)
	}

	b.pump.pumpUntil(t, 2*time.Second, "one error tab", func() bool {
		return len(b.tabs.errorCalls) == 1
	})
	if b.tabs.errorCalls[0].Err != data.ErrNoSession {
		t.Fatalf("error tab err = %v, want ErrNoSession", b.tabs.errorCalls[0].Err)
	}
	if len(b.tabs.resultCalls) != 0 {
		t.Fatalf("result tabs = %d, want 0", len(b.tabs.resultCalls))
	}
}

// TestRapidEnterSpamAsyncSettles: 25 rapid single-statement Enters are
// 25 separate last-wins actions — the queued predecessors are cancelled
// via their sentinels, only the last runs (one stream, one tab), and
// every run scope settles (each start eventually gets its finish/end on
// the UI thread; the mismatched ones no-op inside NoticeHelper).
func TestRapidEnterSpamAsyncSettles(t *testing.T) {
	sess := &launchInner{}
	b := newAsyncBag(sess)
	ctrl := b.controller(&fakeEditorBuffer{Text: "SELECT 1;", Off: 3})
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	cmd, _ := reg.Get(commands.QueryRun)
	for i := 0; i < 25; i++ {
		if err := cmd.Handler(commands.ExecCtx{}); err != nil {
			t.Fatalf("Run handler #%d err = %v", i, err)
		}
	}
	b.pump.pumpUntil(t, 4*time.Second, "all 25 run scopes opened", func() bool {
		return countPrefix(b.notice.snapshot(), "start:") == 25
	})
	// Quiesce: with a real SQLSession wrapping the fake, a fast early
	// action may legitimately complete before its successor's enqueue
	// cancels it — wait until nothing changes, then assert the
	// last-wins INVARIANTS rather than an exact count.
	var tabs, streams, calls int
	for i := 0; i < 30; i++ {
		b.pump.drain()
		time.Sleep(20 * time.Millisecond)
		t2, s2, c2 := len(b.tabs.resultCalls), countSuffix(sess.snapshot(), ":start"), len(b.notice.snapshot())
		if t2 == tabs && s2 == streams && c2 == calls && i > 2 {
			break
		}
		tabs, streams, calls = t2, s2, c2
	}
	if tabs < 1 || tabs > 25 {
		t.Fatalf("tabs = %d, want 1..25 (last-wins suppressed the rest)", tabs)
	}
	// Bounded work: only a handful of actions ever reached the session —
	// the rest were sentinel-cancelled while queued. (A completed action
	// that was abandoned mid-op also counts a stream without a tab, so
	// streams only bounds, not equals, tabs.)
	if streams < 1 || streams >= 25 {
		t.Fatalf("session streams started = %d, want 1..24 (last-wins bounds the work)", streams)
	}
	callsList := b.notice.snapshot()
	starts, attaches, finishes := 0, 0, 0
	for _, c := range callsList {
		switch {
		case strings.HasPrefix(c, "start:"):
			starts++
		case c == "attach":
			attaches++
		case strings.HasPrefix(c, "finish:"):
			finishes++
		}
	}
	if starts != 25 {
		t.Fatalf("notice starts = %d, want 25 (one per action)", starts)
	}
	if attaches != tabs || finishes != tabs {
		t.Fatalf("notice attaches = %d, finishes = %d, want %d/%d (one per surviving action)", attaches, finishes, tabs, tabs)
	}
}

// countSuffix counts events ending with suffix.
func countSuffix(events []string, suffix string) int {
	n := 0
	for _, ev := range events {
		if strings.HasSuffix(ev, suffix) {
			n++
		}
	}
	return n
}

// countPrefix counts notice calls beginning with prefix.
func countPrefix(calls []string, prefix string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// TestReRunActiveTabAsyncReattachesOnUIThread covers the sort re-run
// path: with a scheduler wired, reRunActiveTab goes through the launch
// queue and the ReattachActiveTab + AttachStream continuation arrives on
// the UI thread.
func TestReRunActiveTabAsyncReattachesOnUIThread(t *testing.T) {
	sess := &launchInner{}
	b := newAsyncBag(sess)
	tabs := &reattachTabs{}
	base := newBag()
	base.HelperBag.QueryRunner = b.runner
	base.HelperBag.EditorBuffer = &fakeEditorBuffer{Text: "SELECT 1;", Off: 3}
	base.HelperBag.ResultTabs = tabs
	base.HelperBag.Toast = b.toast
	base.HelperBag.Notice = b.notice
	base.HelperBag.ThreadingDeps = controllers.ThreadingDeps{
		OnUIThread:            b.pump.post,
		OnUIThreadContentOnly: b.pump.post,
		OnWorker: func(fn func(gocui.Task) error) {
			_ = fn(nil)
		},
	}
	ctrl := controllers.NewQueryEditorController(nil, base.HelperBag.CoreDeps, base.HelperBag.NavDeps, base.HelperBag.UIDeps, base.HelperBag.QueryDeps, base.HelperBag.ThreadingDeps)
	_ = ctrl

	// The constructor wired sortActiveResult into the tabs hook; drive it.
	tabs.mu.Lock()
	hook := tabs.sortHook
	tabs.mu.Unlock()
	if hook == nil {
		t.Fatal("sort hook not wired by the constructor")
	}
	hook(0)

	b.pump.pumpUntil(t, 2*time.Second, "one reattach", func() bool {
		tabs.mu.Lock()
		defer tabs.mu.Unlock()
		return len(tabs.reattached) == 1
	})
	tabs.mu.Lock()
	got := tabs.reattached[0]
	tabs.mu.Unlock()
	if got.RunSQL != "SELECT orig ORDER BY 1" || got.OrigSQL != "SELECT orig" {
		t.Fatalf("reattach = (%q, %q), want the sort SQL with the write-once origin preserved", got.RunSQL, got.OrigSQL)
	}
	// The notice stream attached for the re-run.
	attached := false
	for _, c := range b.notice.snapshot() {
		if c == "attach" {
			attached = true
		}
	}
	if !attached {
		t.Fatal("AttachStream never fired for the re-run")
	}
}

// TestNoticeHelperAttachStreamWithoutRunWarns covers the C2
// observability guard on the REAL NoticeHelper: AttachStream firing with
// a real RunHandle while no run is bound drops the stream's NOTICEs
// silently, so it must warn-log. (Lives here — not notice_helper_test —
// because reaching the branch needs a real *session.RunHandle, which
// this package's launchInner fixture produces.)
func TestNoticeHelperAttachStreamWithoutRunWarns(t *testing.T) {
	sess := &launchInner{}

	// Obtain a real RunHandle via the session the bag already wired.
	rh, err := session.New(&launchConn{}, sess, session.Options{}).Stream(stdcontext.Background(), models.Query{SQL: "SELECT 1"})
	if err != nil || rh == nil {
		t.Fatalf("staging Stream = (%v, %v), want a real RunHandle", rh, err)
	}

	handler := logs.NewRecordingHandler()
	h := ui.NewNoticeHelper(ui.NoticeHelperDeps{
		Tr:     i18n.EnglishTranslationSet(),
		Logger: slog.New(handler),
	})
	// No OnRunStart: currentRun is "" — the early-return path.
	h.AttachStream(rh)

	recs := handler.Records()
	if len(recs) != 1 {
		t.Fatalf("records = %d, want exactly 1 warn", len(recs))
	}
	if recs[0].Level != slog.LevelWarn {
		t.Fatalf("level = %v, want Warn", recs[0].Level)
	}
	var evt string
	recs[0].Attrs(func(a slog.Attr) bool {
		if a.Key == "evt" {
			evt = a.Value.String()
		}
		return true
	})
	if evt != "notice_attach_no_run" {
		t.Fatalf("evt = %q, want notice_attach_no_run", evt)
	}

	// A nil Logger must not panic on the same path.
	ui.NewNoticeHelper(ui.NoticeHelperDeps{Tr: i18n.EnglishTranslationSet()}).AttachStream(rh)
}

// TestQueryEditorExplainAnalyzeAsyncReturnsAfterEnqueue is C4 at the
// controller seam: with a UI scheduler wired, the <leader>eA handler
// returns after enqueue (under the 50ms budget) while the EXPLAIN ANALYZE
// op is parked on the launcher — the statement duration (RC3) must not
// block the UI thread. The plan-tab continuation then arrives on the UI
// thread once the op resolves.
func TestQueryEditorExplainAnalyzeAsyncReturnsAfterEnqueue(t *testing.T) {
	sess := &launchInner{}
	gate := make(chan struct{})
	sess.explainGate = gate
	b := newAsyncBag(sess)
	ctrl := b.controller(&fakeEditorBuffer{Text: "SELECT pg_sleep(5);", Off: 3})
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	cmd, _ := reg.Get(commands.QueryExplainAnalyze)

	start := time.Now()
	handlerDone := make(chan error, 1)
	go func() { handlerDone <- cmd.Handler(commands.ExecCtx{}) }()
	select {
	case err := <-handlerDone:
		if err != nil {
			t.Fatalf("ExplainAnalyze handler err = %v", err)
		}
		if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
			t.Fatalf("handler took %vms — want enqueue-only, <50ms (EXPLAIN ANALYZE must not run on the UI thread)", elapsed.Milliseconds())
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("handler still blocked after 100ms while the EXPLAIN ANALYZE op is parked — EXPLAIN runs on the UI thread")
	}
	// The plan tab must NOT open until the op resolves on the launcher
	// (the continuation arrives on the UI thread, which we hold).
	if len(b.tabs.planCalls) != 0 {
		t.Fatalf("plan tab opened before the explain op resolved: %#v", b.tabs.planCalls)
	}

	close(gate)
	b.pump.pumpUntil(t, 2*time.Second, "one plan tab", func() bool {
		return len(b.tabs.planCalls) == 1
	})
	if b.tabs.planCalls[0].Label != "SELECT pg_sleep(5)" {
		t.Fatalf("plan tab label = %q, want the statement label", b.tabs.planCalls[0].Label)
	}
}

// TestQueryEditorExplainAsyncErrNoSessionSurfacesMarshalled covers the
// async error path: an ExplainAsync op that loses its session surfaces
// ErrNoSession via the marshalled surfaceErr (error tab) on the UI
// thread — never on the launcher goroutine.
func TestQueryEditorExplainAsyncErrNoSessionSurfacesMarshalled(t *testing.T) {
	sess := &launchInner{explainErr: data.ErrNoSession}
	b := newAsyncBag(sess)
	ctrl := b.controller(&fakeEditorBuffer{Text: "SELECT 1;", Off: 3})
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	cmd, _ := reg.Get(commands.QueryExplain)
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Explain handler err = %v", err)
	}

	b.pump.pumpUntil(t, 2*time.Second, "one error tab", func() bool {
		return len(b.tabs.errorCalls) == 1
	})
	if b.tabs.errorCalls[0].Err != data.ErrNoSession {
		t.Fatalf("error tab err = %v, want ErrNoSession", b.tabs.errorCalls[0].Err)
	}
	if len(b.tabs.planCalls) != 0 {
		t.Fatalf("plan tabs = %d, want 0 on error", len(b.tabs.planCalls))
	}
}

// --- pgsavvy-vky3.3: query-run signal set/clear at the controller seam ---
//
// These tests wire the runner's SetQueryRunSignal seam (the same one
// wire_result_tabs.go bridges onto the Gui's run slot) and assert the
// controller lifecycle contract: NotifyQueryRunStarted at every
// confirmed launch (adjacent to Notice.OnRunStart, same runID) and
// NotifyQueryRunFinished exactly once per action at finishRunScope.

// runSignalSlot mirrors the orchestrator's generation-tagged query-run
// slot semantics (the REAL slot's set-overwrites /
// clear-only-on-match discipline is proven in
// orchestrator/query_run_state_test.go — this copy exists only
// because this package deliberately does not import orchestrator):
// set overwrites the current runID; clear(runID) empties the slot only
// on a match, so a stale settle cannot wipe a newer run.
//
// It doubles as an ordering witness against fakeNoticeReporter: the
// launch sites fire NotifyQueryRunStarted immediately after
// Notice.OnRunStart, and finishRunScope fires NotifyQueryRunFinished
// immediately before Notice.Finish/OnRunEnd — all on the same
// goroutine — so the notice snapshot taken inside each hook is a stable
// bracket witness. Each recorded call carries that evidence as a
// "@in-start-bracket" / "@before-end-bracket" (or "...@outside-...")
// suffix.
type runSignalSlot struct {
	mu     sync.Mutex
	calls  []string
	cur    string
	notice *fakeNoticeReporter
}

// lastNoticeIs reports whether the reporter's most recent call is want.
func lastNoticeIs(n *fakeNoticeReporter, want string) bool {
	if n == nil {
		return false
	}
	calls := n.snapshot()
	return len(calls) > 0 && calls[len(calls)-1] == want
}

// noticeHas reports whether the reporter ever recorded want.
func noticeHas(n *fakeNoticeReporter, want string) bool {
	if n == nil {
		return false
	}
	for _, c := range n.snapshot() {
		if c == want {
			return true
		}
	}
	return false
}

func (s *runSignalSlot) onSet(runID string) {
	tag := "outside-start-bracket"
	if lastNoticeIs(s.notice, "start:"+runID) {
		tag = "in-start-bracket"
	}
	s.mu.Lock()
	s.calls = append(s.calls, "set:"+runID+"@"+tag)
	s.cur = runID
	s.mu.Unlock()
}

func (s *runSignalSlot) onClear(runID string) {
	tag := "after-end-bracket"
	if !noticeHas(s.notice, "finish:"+runID) && !noticeHas(s.notice, "end:"+runID) {
		tag = "before-end-bracket"
	}
	s.mu.Lock()
	s.calls = append(s.calls, "clear:"+runID+"@"+tag)
	if s.cur == runID {
		s.cur = ""
	}
	s.mu.Unlock()
}

// current reports the slot's live runID (generation-slot semantics).
func (s *runSignalSlot) current() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur, s.cur != ""
}

func (s *runSignalSlot) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.calls))
	copy(out, s.calls)
	return out
}

// tapQueryRunSignal wires the bag's runner query-run signal onto a
// fresh generation-slot mirror and returns it for assertions.
func tapQueryRunSignal(b *asyncBag) *runSignalSlot {
	s := &runSignalSlot{notice: b.notice}
	b.runner.SetQueryRunSignal(s.onSet, s.onClear)
	return s
}

// queued reports how many closures the pump is holding unrun.
func (p *uiPump) queued() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.fns)
}

// drainOne runs exactly ONE queued closure (FIFO), leaving any
// siblings queued behind it unrun — the observation point for
// "state persists through intermediate settles".
func (p *uiPump) drainOne(t *testing.T) {
	t.Helper()
	p.mu.Lock()
	if len(p.fns) == 0 {
		p.mu.Unlock()
		t.Fatal("drainOne: pump queue empty")
	}
	fn := p.fns[0]
	p.fns = p.fns[1:]
	p.mu.Unlock()
	_ = fn()
}

// startedRunIDs extracts the runIDs of every "start:" notice call, in
// launch order.
func startedRunIDs(calls []string) []string {
	var ids []string
	for _, c := range calls {
		if strings.HasPrefix(c, "start:") {
			ids = append(ids, strings.TrimPrefix(c, "start:"))
		}
	}
	return ids
}

// TestQueryRunSignalSingleRunSetClearOnce: a single <leader>r fires the
// set at launch with the SAME runID as the Notice start bracket, and
// the clear exactly once at finishRunScope — inside the finish bracket
// (before Notice.Finish records), with nothing between set and clear
// but the attach.
func TestQueryRunSignalSingleRunSetClearOnce(t *testing.T) {
	sess := &launchInner{}
	b := newAsyncBag(sess)
	slot := tapQueryRunSignal(b)
	ctrl := b.controller(&fakeEditorBuffer{Text: "SELECT 1;", Off: 3})
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	cmd, _ := reg.Get(commands.QueryRun)
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Run handler err = %v", err)
	}

	b.pump.pumpUntil(t, 2*time.Second, "run settled", func() bool {
		return countPrefix(b.notice.snapshot(), "finish:") == 1
	})

	ids := startedRunIDs(b.notice.snapshot())
	if len(ids) != 1 {
		t.Fatalf("started runIDs = %v, want exactly 1", ids)
	}
	runID := ids[0]
	want := []string{
		"set:" + runID + "@in-start-bracket",
		"clear:" + runID + "@before-end-bracket",
	}
	got := slot.snapshot()
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("signal calls = %v, want %v (set once in the start bracket, clear once before the finish)", got, want)
	}
	if cur, ok := slot.current(); ok {
		t.Fatalf("slot reports %q after settle, want idle", cur)
	}
}

// TestQueryRunSignalRunSQLSetsAndClears: the RunSQL path (TABLES <cr>
// reuse) sets at launch and clears at finishRunScope with the same
// runID, exactly once each.
func TestQueryRunSignalRunSQLSetsAndClears(t *testing.T) {
	sess := &launchInner{}
	b := newAsyncBag(sess)
	slot := tapQueryRunSignal(b)
	ctrl := b.controller(&fakeEditorBuffer{Text: "SELECT 1;"})
	if !ctrl.RunSQL("SELECT * FROM t") {
		t.Fatal("RunSQL returned false with an active session, want true")
	}

	b.pump.pumpUntil(t, 2*time.Second, "run settled", func() bool {
		return countPrefix(b.notice.snapshot(), "finish:") == 1
	})

	ids := startedRunIDs(b.notice.snapshot())
	if len(ids) != 1 {
		t.Fatalf("started runIDs = %v, want exactly 1", ids)
	}
	runID := ids[0]
	got := slot.snapshot()
	if len(got) != 2 {
		t.Fatalf("signal calls = %v, want exactly set+clear for %q", got, runID)
	}
	if got[0] != "set:"+runID+"@in-start-bracket" {
		t.Fatalf("set call = %q, want set of %q inside the start bracket", got[0], runID)
	}
	if got[1] != "clear:"+runID+"@before-end-bracket" {
		t.Fatalf("clear call = %q, want clear of %q before the finish records", got[1], runID)
	}
	if cur, ok := slot.current(); ok {
		t.Fatalf("slot reports %q after settle, want idle", cur)
	}
}

// TestQueryRunSignalCancelPathClearsAtFinishScope: <leader>x against a
// parked launch cancels it; the canceled ack (context.Canceled,
// nothing attached) still funnels through finishRunScope, so the clear
// fires with the canceled run's ID before Notice.OnRunEnd records.
func TestQueryRunSignalCancelPathClearsAtFinishScope(t *testing.T) {
	sess := &launchInner{}
	gate := make(chan struct{})
	sess.stage(gate)
	b := newAsyncBag(sess)
	slot := tapQueryRunSignal(b)
	ctrl := b.controller(&fakeEditorBuffer{Text: "SELECT 1;", Off: 3})
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	runCmd, _ := reg.Get(commands.QueryRun)
	if err := runCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Run handler err = %v", err)
	}

	// The launch is parked inside Stream; the start bracket is already
	// recorded (the proceed closure ran synchronously in the handler).
	ids := startedRunIDs(b.notice.snapshot())
	if len(ids) != 1 {
		t.Fatalf("started runIDs = %v before cancel, want exactly 1", ids)
	}
	runID := ids[0]

	cancelCmd, _ := reg.Get(commands.QueryCancel)
	if err := cancelCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Cancel handler err = %v", err)
	}
	b.pump.pumpUntil(t, 2*time.Second, "canceled run settled", func() bool {
		return noticeHas(b.notice, "end:"+runID)
	})

	got := slot.snapshot()
	if len(got) != 2 {
		t.Fatalf("signal calls = %v, want exactly set+clear for %q", got, runID)
	}
	if got[0] != "set:"+runID+"@in-start-bracket" {
		t.Fatalf("set call = %q, want set of %q inside the start bracket", got[0], runID)
	}
	if got[1] != "clear:"+runID+"@before-end-bracket" {
		t.Fatalf("clear call = %q, want clear of %q fired at finishRunScope before OnRunEnd", got[1], runID)
	}
	if cur, ok := slot.current(); ok {
		t.Fatalf("slot reports %q after canceled settle, want idle", cur)
	}
	// Canceled: nothing attached, no tabs of either kind.
	if len(b.tabs.resultCalls) != 0 || len(b.tabs.errorCalls) != 0 {
		t.Fatalf("tabs = %d result / %d error, want 0/0 on a canceled launch", len(b.tabs.resultCalls), len(b.tabs.errorCalls))
	}
}

// TestQueryRunSignalLastWinsRetainsNewerRun: launch A (parked), launch
// B — B's launch preempts A, so A's settle is stale. Pumping BOTH
// settles, the slot still reports B's runID after A's stale clear, and
// only clears when B's own finishRunScope fires. Positive assertion on
// the retained state, not merely "empty at end".
func TestQueryRunSignalLastWinsRetainsNewerRun(t *testing.T) {
	sess := &launchInner{}
	g1, g2 := make(chan struct{}), make(chan struct{})
	sess.stage(g1)
	sess.stage(g2)
	b := newAsyncBag(sess)
	slot := tapQueryRunSignal(b)
	ctrl := b.controller(&fakeEditorBuffer{Text: "SELECT 1;", Off: 3})
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	runCmd, _ := reg.Get(commands.QueryRun)
	if err := runCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Run handler A err = %v", err)
	}

	// Wait until A's Stream actually started (it consumed gate 1), so
	// B's launch deterministically parks on gate 2.
	deadline := time.Now().Add(2 * time.Second)
	for countSuffix(sess.snapshot(), ":start") < 1 {
		if time.Now().After(deadline) {
			t.Fatal("run A never reached its Stream call")
		}
		time.Sleep(time.Millisecond)
	}

	if err := runCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Run handler B err = %v", err)
	}
	ids := startedRunIDs(b.notice.snapshot())
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("started runIDs = %v, want two distinct IDs", ids)
	}
	runA, runB := ids[0], ids[1]

	// A is preempt-canceled: its sentinel is cancelled by B's enqueue,
	// but the mid-op session call itself runs to completion (pgx-safe
	// single-flight), so release A's gate — its abandoned rh is then
	// closed and the ack surfaces context.Canceled → settle → clear(A).
	close(g1)
	b.pump.pumpUntil(t, 2*time.Second, "preempted A settled", func() bool {
		return noticeHas(b.notice, "end:"+runA)
	})

	// POSITIVE last-wins: after A's stale clear, the slot still holds B.
	cur, ok := slot.current()
	if !ok || cur != runB {
		t.Fatalf("slot = (%q, %v) after stale clear of A, want (%q, true) — B retained", cur, ok, runB)
	}

	// Release B: it settles, attaches, and its OWN finishRunScope clears.
	close(g2)
	b.pump.pumpUntil(t, 2*time.Second, "run B settled", func() bool {
		return noticeHas(b.notice, "finish:"+runB)
	})
	if cur, ok := slot.current(); ok {
		t.Fatalf("slot reports %q after B settled, want idle", cur)
	}

	got := slot.snapshot()
	if len(got) != 4 {
		t.Fatalf("signal calls = %v, want set+clear for each of A and B", got)
	}
	if got[0] != "set:"+runA+"@in-start-bracket" || got[1] != "set:"+runB+"@in-start-bracket" {
		t.Fatalf("set calls = %v, want A then B in the start brackets", got[:2])
	}
	// A's clear precedes B's in settle order; both fire before their
	// Notice end brackets. Ordering of clear(A) vs attach(B) is
	// scheduler-dependent — only bracket + match semantics are pinned.
	for _, c := range got[2:] {
		if !strings.HasPrefix(c, "clear:") || !strings.HasSuffix(c, "@before-end-bracket") {
			t.Fatalf("clear call = %q, want a bracket-preceding clear", c)
		}
	}
}

// TestQueryRunSignalRunAllPersistsUntilBatchEnd: a run-all fan-out sets
// once at batch launch; the slot STAYS set through the intermediate
// per-statement settles (asserted after the first statement's settle
// closure runs while the batch is still in flight) and clears only at
// the batch-end finishRunScope.
func TestQueryRunSignalRunAllPersistsUntilBatchEnd(t *testing.T) {
	sess := &launchInner{}
	b := newAsyncBag(sess)
	slot := tapQueryRunSignal(b)
	ctrl := b.controller(&fakeEditorBuffer{Text: "SELECT 1; SELECT 2; SELECT 3;"})
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	cmd, _ := reg.Get(commands.QueryRunAll)
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("RunAll handler err = %v", err)
	}

	// Wait for the first per-statement settle closure to queue, then
	// run exactly ONE — the batch is provably mid-flight.
	deadline := time.Now().Add(2 * time.Second)
	for b.pump.queued() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no settle closure was ever posted for statement 1")
		}
		time.Sleep(time.Millisecond)
	}
	b.pump.drainOne(t)

	// First statement settled (tab + attach landed); the batch's
	// finishRunScope has NOT fired — the slot must still hold the run.
	if len(b.tabs.resultCalls) != 1 {
		t.Fatalf("result tabs = %d after first settle, want 1", len(b.tabs.resultCalls))
	}
	ids := startedRunIDs(b.notice.snapshot())
	if len(ids) != 1 {
		t.Fatalf("started runIDs = %v, want the single batch run", ids)
	}
	cur, ok := slot.current()
	if !ok || cur != ids[0] {
		t.Fatalf("slot = (%q, %v) after first statement settled, want (%q, true) — batch still running", cur, ok, ids[0])
	}

	// Pump the rest: the batch-end finishRunScope is the ONLY clear.
	b.pump.pumpUntil(t, 2*time.Second, "batch settled", func() bool {
		return countPrefix(b.notice.snapshot(), "finish:") == 1
	})
	got := slot.snapshot()
	if len(got) != 2 || got[0] != "set:"+ids[0]+"@in-start-bracket" || got[1] != "clear:"+ids[0]+"@before-end-bracket" {
		t.Fatalf("signal calls = %v, want one set at launch and one clear at batch end for %q", got, ids[0])
	}
	if cur, ok := slot.current(); ok {
		t.Fatalf("slot reports %q after batch end, want idle", cur)
	}
}

// TestQueryRunSignalFastSettleIdlesInOnePumpCycle: an immediately
// resolving statement needs no gates or ticks — one pump cycle carries
// the settle, so the clear follows the set promptly and the slot
// reports idle.
func TestQueryRunSignalFastSettleIdlesInOnePumpCycle(t *testing.T) {
	sess := &launchInner{}
	b := newAsyncBag(sess)
	slot := tapQueryRunSignal(b)
	ctrl := b.controller(&fakeEditorBuffer{Text: "SELECT 1;", Off: 3})
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	cmd, _ := reg.Get(commands.QueryRun)
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Run handler err = %v", err)
	}

	b.pump.pumpUntil(t, 2*time.Second, "fast run settled", func() bool {
		return countPrefix(b.notice.snapshot(), "finish:") == 1
	})

	got := slot.snapshot()
	if len(got) != 2 {
		t.Fatalf("signal calls = %v, want set immediately followed by clear", got)
	}
	if !strings.HasPrefix(got[0], "set:") || !strings.HasPrefix(got[1], "clear:") {
		t.Fatalf("signal calls = %v, want set before clear", got)
	}
	if cur, ok := slot.current(); ok {
		t.Fatalf("slot reports %q after the same-cycle settle, want idle", cur)
	}
}

// TestQueryEditorExplainAsyncOpensPlanTabMarshalled proves the happy path
// continuation (notice toast + plan tab) arrives on the UI thread via the
// pump, in ack order.
func TestQueryEditorExplainAsyncOpensPlanTabMarshalled(t *testing.T) {
	sess := &launchInner{explainPlan: models.Plan{RawText: "real plan", Notice: "degraded"}}
	b := newAsyncBag(sess)
	ctrl := b.controller(&fakeEditorBuffer{Text: "SELECT 1;", Off: 3})
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	cmd, _ := reg.Get(commands.QueryExplain)
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Explain handler err = %v", err)
	}

	b.pump.pumpUntil(t, 2*time.Second, "one plan tab", func() bool {
		return len(b.tabs.planCalls) == 1
	})
	if got := b.tabs.planCalls[0].Plan.RawText; got != "real plan" {
		t.Fatalf("plan tab RawText = %q, want real plan", got)
	}
	b.pump.pumpUntil(t, 2*time.Second, "notice toast", func() bool {
		return len(b.toast.msgs) == 1
	})
	if got := b.toast.msgs[0].Msg; got != "degraded" {
		t.Fatalf("notice toast = %q, want degraded", got)
	}
}
