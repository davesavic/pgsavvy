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
