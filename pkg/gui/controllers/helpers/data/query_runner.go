package data

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
)

// ErrNoSession is returned by QueryRunner methods when no SQLSession is
// wired (typically because the user is not connected yet).
var ErrNoSession = errors.New("query: no active session")

// RunnerSession is the subset of *session.SQLSession that QueryRunner
// needs. Defining the dependency as an interface keeps the helper
// testable without a live driver: tests inject a fake that records
// calls and returns canned RunHandles / Plans.
//
// *session.SQLSession satisfies this interface; the compile-time check
// below pins that contract. Exported so tests outside the data
// package can build a runner backed by a fake.
type RunnerSession interface {
	Execute(ctx context.Context, q models.Query) (models.Result, error)
	Stream(ctx context.Context, q models.Query) (*session.RunHandle, error)
	Explain(ctx context.Context, q models.Query, analyze bool) (models.Plan, error)
	Begin(ctx context.Context, opts models.TxOptions) (drivers.Transaction, error)
	InTransaction() bool
	CurrentTransaction() drivers.Transaction
	LiveTxStatus() (models.TxStatus, []string)
	Cancel(qid models.QueryID) error
	SetDisconnected(bool)
	IsDisconnected() bool
	MarkPreemptPending()
}

var _ RunnerSession = (*session.SQLSession)(nil)

// RunOptions tweaks a single Run call. NewTx wraps the statement in an
// explicit BEGIN issued before the Stream; the surrounding transaction
// is left open and rolled back when the session is closed (SQLSession
// already rolls back any active tx in Close).
type RunOptions struct {
	NewTx bool

	// DefaultSchema is the currently selected schema; when non-empty it is
	// forwarded on the streamed Query so unqualified object names resolve
	// against it (pg: SET search_path). Empty leaves resolution unchanged.
	DefaultSchema string

	// Timeout is the resolved statement-timeout ceiling forwarded onto the
	// streamed Query. The run path sets it from config.query
	// .default_statement_timeout (0 = off). The pg driver realises a
	// non-zero value as a context.WithTimeout deadline whose CancelFunc the
	// row stream owns; 0 leaves the caller's context untouched (no ceiling).
	Timeout time.Duration
}

// runnerBinding is the (sess, caps) pair swapped atomically by Bind /
// Unbind. Stored as an immutable value pointed at by binding so reads
// see a consistent snapshot — partial publication of one field without
// the other is impossible.
type runnerBinding struct {
	sess RunnerSession
	caps drivers.Capabilities
}

// QueryRunner orchestrates the streaming-query lifecycle on behalf of
// the QueryEditorController: it dispatches Execute/Stream/Explain via
// the SQLSession queue and exposes a single Cancel handle that targets
// the last launched RunHandle.
//
// QueryRunner is intentionally narrow — it neither owns the result-tab
// state nor tracks history. Tab routing is owned by ResultTabsHelper;
// history is recorded transparently by SQLSession.
//
// Threading: every method delegates to SQLSession, which serialises
// against the per-session queue. Concurrent calls into QueryRunner are
// safe; they queue inside SQLSession. Bind / Unbind swap the inner
// session atomically so a controller value-copy of the helper bag (the
// runner pointer) keeps seeing the freshest binding after a Connect.
//
// UI-goroutine contract: Run, RunQuery, Begin, and Explain are
// synchronous and MUST be called on a goroutine that is allowed to block
// (a worker, or a test) — each invokes the preempt hook (see SetPreempter)
// as its first action, which is bounded but slow. RunAsync / RunQueryAsync are the UI
// thread entry points: they enqueue onto the single-flight launch queue
// and return immediately. The sentinel they publish in last BEFORE
// enqueueing is what keeps a tab-less launch last-wins cancellable: the
// next enqueue (or any preemptInFlight) resolves and cancels it on the
// spot, without touching the session.
//
// The launch queue preserves the multi-op sequence exclusivity the
// synchronous UI-goroutine contract used to give Run(NewTx) for free:
// Begin→Stream (:NewTx) runs as ONE op on the single launcher goroutine,
// so a later launch can never land its DML inside another launch's
// wrap-tx (silent rollback = data loss).
//
// preempt lives directly on *QueryRunner (NOT on runnerBinding) so it
// survives the atomic Bind / Unbind swap — a reconnect must not silently
// drop the preempter and reintroduce the freeze. It is stored behind an
// atomic pointer: the launcher goroutine reads it while the UI thread
// may still be wiring it.
type QueryRunner struct {
	binding atomic.Pointer[runnerBinding]

	// last holds the most recent run: a PENDING launch sentinel (cancel
	// non-nil, rh nil) published on the enqueueing goroutine before the
	// request is queued, or a RESOLVED slot carrying the RunHandle once
	// a session op returned. Cancel / CancelAndWaitActiveRun /
	// preemptInFlight resolve whichever flavour is present.
	last atomic.Pointer[runSlot]

	// preempt, when non-nil, stops any in-flight result-tab stream before
	// a new session op acquires the per-session queue lock. Set once at
	// wire time via SetPreempter; nil in unit tests that don't exercise
	// preemption. Returns true when the bounded Stop-wait EXPIRED — the
	// prior worker is still live and streamMu is still held — so the caller
	// fences the session (MarkPreemptPending) and the guarded session op
	// below fails fast with ErrPreemptPending.
	preemptHook atomic.Pointer[func() bool]

	// launchMu guards the launch queue and the launcher-liveness flag.
	launchMu  sync.Mutex
	launchQ   []*launchRequest
	launching bool
}

// runSlot is one entry of QueryRunner.last. A slot is immutable once
// published; state transitions swap in a NEW slot via CompareAndSwap so
// exactly one of {canceller, resolver} wins:
//
//   - pending sentinel: ctx/cancel/done set, rh nil. Created on the
//     enqueueing (UI) thread before the request is queued.
//   - resolved slot: rh set (may be nil when the op errored or the fake
//     returned no handle), cancel nil — a resolved launch is never
//     context-cancelled (that would kill the live row stream sharing the
//     sentinel ctx). done is nil; waiters use rh.Done().
//
// done closes only when the launch has resolved AND the resulting
// RunHandle has terminated (watcher goroutine), so a quit-during-gap
// CancelAndWait cannot let a Commit overtake the drain.
type runSlot struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	rh     *session.RunHandle

	// abandoned is set by the goroutine that exclusively removed a
	// pending sentinel from last. A launch that resolves after being
	// abandoned is suppressed from its ack as a cancellation (last-wins)
	// and its RunHandle is closed promptly. Suppression is a best-effort
	// check-then-act, not atomic with the removal: a launch resolving
	// concurrently with its abandonment may surface a success ack —
	// harmless, since the successor action's preempt hook stops its tab.
	abandoned atomic.Bool
}

// launchRequest is one queued user action: the statements to run (each
// with its own op), whether the preempt-first chokepoint fires before
// each statement, and the ack (the reply) invoked on the launcher
// goroutine with each statement's result. One action = one request, so
// last-wins cancellation at enqueue time can only ever target a PRIOR
// action — a fan-out's own statements never cancel each other, and the
// whole batch is atomic with respect to any later launch.
type launchRequest struct {
	slot         *runSlot
	preemptFirst bool
	stmts        []launchStmt
	ack          func(index int, rh *session.RunHandle, err error)
}

// launchStmt is one statement of a batch launch.
type launchStmt struct {
	op func(ctx context.Context) (*session.RunHandle, error)
}

// NewQueryRunner builds a QueryRunner bound to sess. caps captures the
// driver's capability flags at construction time — the controller
// reads QueryRunner.Capabilities().HasLiveCancel at RegisterActions
// time so the <leader>x DisabledReasonStatic stays accurate without a
// re-registration on driver swap (driver swap goes through reconnect →
// bootstrap → fresh QueryRunner).
//
// sess may be nil; every method nil-checks and returns ErrNoSession
// (or, for Cancel, silently no-ops). The orchestrator builds an empty
// QueryRunner at wireWithDriver time and later calls Bind from the
// connectInvoker once the SQLSession is ready.
func NewQueryRunner(sess RunnerSession, caps drivers.Capabilities) *QueryRunner {
	r := &QueryRunner{}
	if sess != nil {
		r.binding.Store(&runnerBinding{sess: sess, caps: caps})
	} else {
		// Preserve caps even when sess is nil so callers that pre-set
		// capabilities before binding (production bootstrap path) still
		// observe them via Capabilities().
		r.binding.Store(&runnerBinding{caps: caps})
	}
	return r
}

// NewQueryRunnerForSession is the production constructor that accepts a
// *session.SQLSession concrete value (the type production bootstrap
// holds) and forwards into NewQueryRunner. Keeps the bootstrap call
// site free of the narrow-interface cast.
func NewQueryRunnerForSession(sess *session.SQLSession, caps drivers.Capabilities) *QueryRunner {
	if sess == nil {
		return NewQueryRunner(nil, caps)
	}
	return NewQueryRunner(sess, caps)
}

// Bind atomically swaps the runner's (sess, caps) to point at the
// supplied SQLSession. Called by the orchestrator's connectInvoker
// after ConnectHelper.Connect succeeds. Safe to call concurrently with
// Run / Explain / Cancel — readers see either the prior binding or the
// new one, never a torn pair.
func (r *QueryRunner) Bind(sess *session.SQLSession, caps drivers.Capabilities) {
	if r == nil {
		return
	}
	if sess == nil {
		r.binding.Store(&runnerBinding{caps: caps})
		return
	}
	r.binding.Store(&runnerBinding{sess: sess, caps: caps})
}

// Unbind atomically swaps the runner back to a nil session and zeroed
// caps. Called by the orchestrator on disconnect / Gui.Close so HasSession
// flips back to false and the controller short-circuits with the
// no-connection toast on the next <leader>r.
func (r *QueryRunner) Unbind() {
	if r == nil {
		return
	}
	r.binding.Store(&runnerBinding{})
	r.last.Store(nil)
}

// SetPreempter installs the hook invoked at the start of Run / RunQuery
// / Explain (and before each queued launch op) to stop any in-flight
// stream before the new session op locks the per-session queue
// (last-wins). Set once at wire time; the hook is stored on the runner
// itself so it survives Bind / Unbind. fn may be nil to clear the hook.
// Safe to call on a nil receiver and concurrently with a live launcher
// (the hook is read through an atomic pointer).
func (r *QueryRunner) SetPreempter(fn func() bool) {
	if r == nil {
		return
	}
	r.preemptHook.Store(&fn)
}

// preemptInFlight invokes the preempt chokepoint for the synchronous
// entry points (Run / RunQuery / Begin / Explain): it resolves + cancels
// any pending (tab-less) launch sentinel — last-wins, this synchronous op
// wins — then runs the launcher-side preempt. See preemptBeforeLaunch.
func (r *QueryRunner) preemptInFlight() {
	if r == nil {
		return
	}
	r.cancelPendingLaunch()
	r.preemptBeforeLaunch()
}

// preemptBeforeLaunch is the launcher-side chokepoint, run before each
// queued statement: it asks the last RESOLVED RunHandle to terminate
// (the previous statement's tab stream, or a pre-tab orphan whose tab
// never opened) and then runs the preempt hook, fencing the session on
// Stop-wait expiry so the guarded session op fails fast with
// ErrPreemptPending rather than deadlocking on streamMu. It deliberately
// does NOT cancel pending sentinels: `last` may belong to a NEWER queued
// action, and last-wins cancellation is decided at enqueue time on the
// UI thread, never by the launcher.
func (r *QueryRunner) preemptBeforeLaunch() {
	if r == nil {
		return
	}
	r.cancelLastResolvedLaunch()
	hook := r.preemptHook.Load()
	if hook == nil || *hook == nil {
		return
	}
	if (*hook)() {
		if b := r.load(); b != nil && b.sess != nil {
			b.sess.MarkPreemptPending()
		}
	}
}

// cancelPendingLaunch atomically removes and cancels the pending
// sentinel currently published in last. The CompareAndSwap makes exactly
// one caller the winner: if the launch resolved first, the CAS fails and
// no cancellation happens — resolution owns the slot from then on, and
// cancellation of a resolved launch goes through the RunHandle's
// out-of-band Cancel instead.
func (r *QueryRunner) cancelPendingLaunch() {
	if r == nil {
		return
	}
	for {
		s := r.last.Load()
		if s == nil || s.cancel == nil {
			return
		}
		if !r.last.CompareAndSwap(s, nil) {
			continue // someone resolved or replaced it; re-load
		}
		s.abandoned.Store(true)
		s.cancel()
		return
	}
}

// cancelLastResolvedLaunch asks the last RESOLVED RunHandle to terminate
// via the driver's out-of-band cancel. This covers the pre-tab orphan: a
// launch whose op resolved but whose result tab has not opened yet (the
// tolerated busy-counter dip) has no tab for the preempt hook to stop,
// so without this the next op would wait on streamMu until the orphan's
// rows drained. Idempotent — Cancel after Done is a no-op — and it
// duplicates the tab preemption the hook already performs, never
// contradicts it.
func (r *QueryRunner) cancelLastResolvedLaunch() {
	if r == nil {
		return
	}
	if s := r.last.Load(); s != nil && s.rh != nil {
		_ = s.rh.Cancel()
	}
}

// load returns the current binding snapshot. Never returns nil — the
// constructor always seeds a binding so the atomic.Pointer is non-nil
// from the first call.
func (r *QueryRunner) load() *runnerBinding {
	if r == nil {
		return nil
	}
	return r.binding.Load()
}

// Capabilities returns the driver capabilities captured at construction
// or via the most recent Bind.
func (r *QueryRunner) Capabilities() drivers.Capabilities {
	b := r.load()
	if b == nil {
		return drivers.Capabilities{}
	}
	return b.caps
}

// HasSession reports whether a SQLSession is wired. Tests / the
// controller use this to short-circuit before invoking a binding's
// handler so users see a "no connection" toast instead of an error.
func (r *QueryRunner) HasSession() bool {
	b := r.load()
	return b != nil && b.sess != nil
}

// MarkDisconnected sets the connection-dead flag on the underlying
// session. Once set, new Execute/Stream/Begin attempts return
// ErrDisconnected. Returns false when no session is wired.
func (r *QueryRunner) MarkDisconnected() bool {
	b := r.load()
	if b == nil || b.sess == nil {
		return false
	}
	b.sess.SetDisconnected(true)
	return true
}

// ClearDisconnected resets the connection-dead flag on the underlying
// session. Called by ReconnectController after a successful Ping or
// reconnect proves the wire is alive again. No-op when no session is
// wired.
func (r *QueryRunner) ClearDisconnected() {
	b := r.load()
	if b == nil || b.sess == nil {
		return
	}
	b.sess.SetDisconnected(false)
}

// IsDisconnected reports whether the underlying session has been marked
// connection-dead. Returns false when no session is wired.
func (r *QueryRunner) IsDisconnected() bool {
	b := r.load()
	if b == nil || b.sess == nil {
		return false
	}
	return b.sess.IsDisconnected()
}

// InTransaction reports whether the underlying session currently has an
// open transaction. Returns false when no session is wired.
func (r *QueryRunner) InTransaction() bool {
	b := r.load()
	return b != nil && b.sess != nil && b.sess.InTransaction()
}

// CurrentTransaction returns the in-progress driver Transaction, or nil
// when no session is wired or no transaction is active.
func (r *QueryRunner) CurrentTransaction() drivers.Transaction {
	b := r.load()
	if b == nil || b.sess == nil {
		return nil
	}
	return b.sess.CurrentTransaction()
}

// LiveTxStatus reports the underlying session's live transaction status for the
// status-bar badge, or ("", nil) when no session is wired. Unlike
// CurrentTransaction it reflects raw-SQL BEGIN/COMMIT/ROLLBACK, not just the
// driver Begin() API.
func (r *QueryRunner) LiveTxStatus() (models.TxStatus, []string) {
	b := r.load()
	if b == nil || b.sess == nil {
		return "", nil
	}
	return b.sess.LiveTxStatus()
}

// TxStatementCount returns the number of statements executed in the
// current transaction, or 0 when no transaction is active.
func (r *QueryRunner) TxStatementCount() int {
	tx := r.CurrentTransaction()
	if tx == nil {
		return 0
	}
	return tx.StatementCount()
}

// SavepointNames returns the savepoint stack of the current transaction,
// or nil when no transaction is active.
func (r *QueryRunner) SavepointNames() []string {
	tx := r.CurrentTransaction()
	if tx == nil {
		return nil
	}
	return tx.Savepoints()
}

// Begin opens a transaction on the underlying session. Calls
// preemptInFlight first to stop any in-flight stream, mirroring the
// Run / Explain chokepoint contract. Returns ErrNoSession when
// no session is wired.
func (r *QueryRunner) Begin(ctx context.Context, opts models.TxOptions) (drivers.Transaction, error) {
	r.preemptInFlight()
	b := r.load()
	if b == nil || b.sess == nil {
		return nil, ErrNoSession
	}
	return b.sess.Begin(ctx, opts)
}

// Run streams sql via the SQLSession queue. When opts.NewTx is true a
// BEGIN is issued via Execute immediately before the Stream; both
// operations queue on the SQLSession serializer (Begin / Execute share
// the queue mutex with Stream).
//
// The returned RunHandle is also stashed for Cancel(). Callers should
// hand it to ResultTabsHelper.OpenResultTab; the tab owns the row
// drain afterwards.
func (r *QueryRunner) Run(ctx context.Context, sql string, opts RunOptions) (*session.RunHandle, error) {
	r.preemptInFlight()
	b := r.load()
	if b == nil || b.sess == nil {
		return nil, ErrNoSession
	}
	if opts.NewTx {
		if _, err := b.sess.Begin(ctx, models.TxOptions{}); err != nil {
			return nil, err
		}
	}
	rh, err := b.sess.Stream(ctx, models.Query{SQL: sql, DefaultSchema: opts.DefaultSchema, Timeout: opts.Timeout})
	if err != nil {
		return nil, err
	}
	r.publishResolved(nil, rh)
	return rh, nil
}

// RunQuery streams q (SQL + bound Args) via the SQLSession queue, mirroring
// Run but allowing parameter placeholders ($1, $2, ...) to be bound at the
// driver. The returned RunHandle is stashed for Cancel(). Used by the
// FKForwardHelper to issue the parameterized parent-table
// SELECT for `gd`.
func (r *QueryRunner) RunQuery(ctx context.Context, q models.Query) (*session.RunHandle, error) {
	r.preemptInFlight()
	b := r.load()
	if b == nil || b.sess == nil {
		return nil, ErrNoSession
	}
	rh, err := b.sess.Stream(ctx, q)
	if err != nil {
		return nil, err
	}
	r.publishResolved(nil, rh)
	return rh, nil
}

// StatementLaunch is one statement of an async batch launch (a single
// <leader>r is a batch of one; run-all / visual fan-out is a batch of N).
type StatementLaunch struct {
	SQL  string
	Opts RunOptions
}

// RunAsync is the UI-thread mirror of Run for a single statement: it
// creates the launch sentinel, publishes it in last, cancels any prior
// pending (tab-less) launch — last-wins, synchronously on the calling
// thread — and enqueues the session op on the single-flight launch
// queue. It returns after enqueue; the (RunHandle, error) reply is
// delivered via ack on the LAUNCHER goroutine. Callers marshal their
// continuation onto the UI thread from inside ack (toastFromWorker
// convention) and MUST NOT block the UI thread waiting for it — there is
// deliberately no attach-ack rendezvous with the mortal MainLoop.
func (r *QueryRunner) RunAsync(ctx context.Context, sql string, opts RunOptions, ack func(rh *session.RunHandle, err error)) {
	if r == nil {
		return
	}
	r.RunStatementsAsync(ctx, []StatementLaunch{{SQL: sql, Opts: opts}}, func(_ int, rh *session.RunHandle, err error) {
		if ack != nil {
			ack(rh, err)
		}
	})
}

// RunQueryAsync is RunAsync for a parameterized Query (the sort re-run
// path). Same sentinel + queue + ack contract as RunAsync.
func (r *QueryRunner) RunQueryAsync(ctx context.Context, q models.Query, ack func(rh *session.RunHandle, err error)) {
	if r == nil {
		return
	}
	op := func(lctx context.Context) (*session.RunHandle, error) {
		if err := lctx.Err(); err != nil {
			return nil, err
		}
		// Detached like RunStatementsAsync's session ctx — see the
		// comment there for why a sentinel cancel must never ctx-abort
		// an in-flight pgx query.
		dctx := context.WithoutCancel(lctx)
		b := r.load()
		if b == nil || b.sess == nil {
			return nil, ErrNoSession
		}
		return b.sess.Stream(dctx, q)
	}
	r.enqueueLaunch([]launchStmt{{op: op}}, func(_ int, rh *session.RunHandle, err error) {
		if ack != nil {
			ack(rh, err)
		}
	})
}

// RunStatementsAsync enqueues ONE user action covering statements as a
// single launch: the launcher runs them strictly in order on one
// goroutine (multi-op sequence exclusivity — a later action can never
// interleave between this action's BEGIN and its Stream), invoking ack
// per statement with that statement's index and result. A statement
// error does NOT abort the batch (mirroring the synchronous run-all
// loop); a cancelled sentinel does — the remaining statements are acked
// with context.Canceled so fan-out aggregations still settle.
func (r *QueryRunner) RunStatementsAsync(ctx context.Context, stmts []StatementLaunch, ack func(index int, rh *session.RunHandle, err error)) {
	if r == nil || len(stmts) == 0 {
		return
	}
	ops := make([]launchStmt, 0, len(stmts))
	for _, s := range stmts {
		stmt := s
		opts := stmt.Opts
		ops = append(ops, launchStmt{op: func(lctx context.Context) (*session.RunHandle, error) {
			// Queued-cancel fence: a launch whose sentinel was cancelled
			// before the launcher reached it never touches the session.
			if err := lctx.Err(); err != nil {
				return nil, err
			}
			// The session call runs on a ctx DETACHED from the sentinel:
			// cancelling a pgx query mid-flight destroys the whole
			// connection ("conn closed"), so a last-wins enqueue must NOT
			// ctx-abort an in-flight op. Instead the op runs to completion
			// (bounded by the statement timeout), and execLaunch promptly
			// Closes + suppresses the abandoned launch's RunHandle —
			// protocol-safe, and the single-flight queue still guarantees
			// the next op begins only after this one resolved.
			dctx := context.WithoutCancel(lctx)
			b := r.load()
			if b == nil || b.sess == nil {
				return nil, ErrNoSession
			}
			if opts.NewTx {
				if _, err := b.sess.Begin(dctx, models.TxOptions{}); err != nil {
					return nil, err
				}
			}
			return b.sess.Stream(dctx, models.Query{SQL: stmt.SQL, DefaultSchema: opts.DefaultSchema, Timeout: opts.Timeout})
		}})
	}
	r.enqueueLaunch(ops, ack)
}

// enqueueLaunch builds the sentinel, performs the synchronous last-wins
// cancellation of any prior pending launch (a whole prior ACTION — this
// request's own statements are safe: they queue behind, never beside),
// publishes THIS launch's slot (so Cancel / CancelAndWaitActiveRun target
// it from the moment the enqueue returns), and hands the request to the
// launcher goroutine — starting one lazily if none is live.
func (r *QueryRunner) enqueueLaunch(stmts []launchStmt, ack func(index int, rh *session.RunHandle, err error)) {
	lctx, cancel := context.WithCancel(context.Background())
	slot := &runSlot{ctx: lctx, cancel: cancel, done: make(chan struct{})}

	// Synchronous (cheap, non-blocking) last-wins: cancel the prior
	// tab-less launch BEFORE this one is queued, so a rapid second Enter
	// kills the first launch before op #2 begins. The expensive preempt
	// (bounded Stop-wait) runs later, on the launcher, before this op.
	r.cancelPendingLaunch()
	r.last.Store(slot)

	req := &launchRequest{slot: slot, preemptFirst: true, stmts: stmts, ack: ack}
	r.launchMu.Lock()
	r.launchQ = append(r.launchQ, req)
	start := !r.launching
	if start {
		r.launching = true
	}
	r.launchMu.Unlock()
	if start {
		go r.launchLoop()
	}
}

// launchLoop drains the launch queue FIFO. It exits as soon as the queue
// is empty (idle exit — no goroutine outlives the work, so runners need
// no teardown and tests stay goleak-clean); the next enqueue starts a
// fresh loop.
func (r *QueryRunner) launchLoop() {
	for {
		r.launchMu.Lock()
		if len(r.launchQ) == 0 {
			r.launching = false
			r.launchMu.Unlock()
			return
		}
		req := r.launchQ[0]
		r.launchQ = r.launchQ[1:]
		r.launchMu.Unlock()
		r.execLaunch(req)
	}
}

// execLaunch runs one request's statements sequentially on the launcher
// goroutine: the preempt chokepoint (skipping the request's own
// sentinel), each session op, per-statement sentinel resolution (Done
// closes only once every statement's RunHandle terminated), and the ack.
func (r *QueryRunner) execLaunch(req *launchRequest) {
	var watchers sync.WaitGroup
	for i, stmt := range req.stmts {
		if req.preemptFirst {
			r.preemptBeforeLaunch()
		}
		rh, err := stmt.op(req.slot.ctx)

		if req.slot.abandoned.Load() {
			// Last-wins: this launch was preempted. Close the orphaned
			// stream promptly (releases the session queue lock without a
			// tab) and surface a cancellation, not a doomed tab.
			if rh != nil {
				_ = rh.Rows().Close()
				rh = nil
			}
			if err == nil {
				err = context.Canceled
			}
		}

		// Resolve per statement: publish the handle unless a NEWER pending
		// launch owns last (its enqueue cancelled this slot).
		r.publishResolved(req.slot, rh)

		// Sentinel Done spans the whole launch: every statement's
		// RunHandle must terminate before it closes.
		if rh != nil {
			watchers.Add(1)
			go func(rh *session.RunHandle) {
				<-rh.Done()
				watchers.Done()
			}(rh)
		}

		if req.ack != nil {
			req.ack(i, rh, err)
		}

		if ctxErr := req.slot.ctx.Err(); ctxErr != nil {
			// The sentinel was cancelled mid-batch (last-wins): ack the
			// remaining statements as cancelled so aggregations settle,
			// then stop — the user replaced this action.
			if req.ack != nil {
				for j := i + 1; j < len(req.stmts); j++ {
					req.ack(j, nil, ctxErr)
				}
			}
			break
		}
	}
	go func() {
		watchers.Wait()
		close(req.slot.done)
	}()
}

// publishResolved publishes a resolved slot for slot's statement result
// without displacing a NEWER pending sentinel some concurrent enqueue
// published while this op ran (that launch owns last now; last-wins).
// cur == slot is the normal pending → resolved transition.
func (r *QueryRunner) publishResolved(slot *runSlot, rh *session.RunHandle) {
	for {
		cur := r.last.Load()
		if cur != nil && cur.cancel != nil && cur != slot {
			return
		}
		if r.last.CompareAndSwap(cur, &runSlot{rh: rh}) {
			return
		}
	}
}

// Explain delegates to SQLSession.Explain. When analyze is true and no
// transaction is currently open the call is wrapped in BEGIN/ROLLBACK
// so a side-effecting ANALYZE never auto-commits (§D14). When a
// transaction is already open the wrap is skipped — the caller's tx
// retains control over commit/rollback.
//
// defaultSchema, when non-empty, resolves unqualified object names against
// that schema for the EXPLAIN'd statement (pg: SET search_path), matching the
// run path so a plan reflects what Run would execute.
func (r *QueryRunner) Explain(ctx context.Context, sql string, analyze bool, defaultSchema string) (models.Plan, error) {
	r.preemptInFlight()
	b := r.load()
	if b == nil || b.sess == nil {
		return models.Plan{}, ErrNoSession
	}
	if !analyze || b.sess.InTransaction() {
		return b.sess.Explain(ctx, models.Query{SQL: sql, DefaultSchema: defaultSchema}, analyze)
	}

	tx, err := b.sess.Begin(ctx, models.TxOptions{})
	if err != nil {
		return models.Plan{}, err
	}
	plan, explainErr := b.sess.Explain(ctx, models.Query{SQL: sql, DefaultSchema: defaultSchema}, analyze)
	// Always ROLLBACK even if Explain errored — the BEGIN would
	// otherwise leak. The rollback error is swallowed because the
	// user-visible failure is the Explain error.
	_ = tx.Rollback(ctx)
	return plan, explainErr
}

// Cancel asks the SQLSession to cancel the last launched run: a resolved
// RunHandle via the driver's live cancel, or a still-pending (tab-less)
// launch via its sentinel ctx. Returns nil when no run has been launched
// or when the driver lacks live-cancel support (the controller already
// gates <leader>x via DisabledReasonStatic; Cancel remains safe to call
// regardless).
func (r *QueryRunner) Cancel() error {
	b := r.load()
	if b == nil || b.sess == nil {
		return nil
	}
	if !b.caps.HasLiveCancel {
		return nil
	}
	s := r.last.Load()
	if s == nil {
		return nil
	}
	if rh := s.rh; rh != nil {
		return b.sess.Cancel(rh.QueryID())
	}
	if s.cancel != nil {
		s.cancel()
	}
	return nil
}

// CancelAndWaitActiveRun cancels the last launched run (if any) and
// blocks until it is fully terminated or 2 seconds elapse, whichever
// comes first. For a pending launch the wait spans the sentinel's Done —
// which closes only when the launch has resolved AND the resulting
// RunHandle terminated — so a quit-during-gap caller cannot proceed to a
// Commit before the in-flight launch has drained. This mirrors the
// cancel-then-wait pattern in SQLSession.Close.
func (r *QueryRunner) CancelAndWaitActiveRun() {
	s := r.last.Load()
	if s == nil {
		return
	}
	var done <-chan struct{}
	if rh := s.rh; rh != nil {
		_ = rh.Cancel()
		done = rh.Done()
	} else if s.cancel != nil {
		s.cancel()
		done = s.done
	}
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}
