package data

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/session"
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
	InTransaction() bool
	Cancel(qid models.QueryID) error
}

var _ RunnerSession = (*session.SQLSession)(nil)

// RunOptions tweaks a single Run call. NewTx wraps the statement in an
// explicit BEGIN issued before the Stream; the surrounding transaction
// is left open and rolled back when the session is closed (SQLSession
// already rolls back any active tx in Close — dbsavvy-66p §D14).
type RunOptions struct {
	NewTx bool

	// DefaultSchema is the currently selected schema; when non-empty it is
	// forwarded on the streamed Query so unqualified object names resolve
	// against it (pg: SET search_path). Empty leaves resolution unchanged
	// (dbsavvy-u1n).
	DefaultSchema string
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
// state nor tracks history. Tab routing is owned by ResultTabsHelper
// (dbsavvy-66p.12); history is recorded transparently by SQLSession.
//
// Threading: every method delegates to SQLSession, which serialises
// against the per-session queue. Concurrent calls into QueryRunner are
// safe; they queue inside SQLSession. Bind / Unbind swap the inner
// session atomically so a controller value-copy of the helper bag (the
// runner pointer) keeps seeing the freshest binding after a Connect.
//
// UI-goroutine contract: Run, RunQuery, and Explain MUST be called on
// the UI goroutine. Each invokes the preempt hook (see SetPreempter) as
// its first action: a prior run whose result exceeds the initial-fill
// window parks its worker holding SQLSession.streamMu indefinitely, so
// the synchronous session op below would freeze the TUI without a
// last-wins preempt first (dbsavvy-lxn). The preempt hook itself must be
// safe to call from the UI goroutine.
//
// preempt lives directly on *QueryRunner (NOT on runnerBinding) so it
// survives the atomic Bind / Unbind swap — a reconnect must not silently
// drop the preempter and reintroduce the freeze (dbsavvy-lxn.1).
type QueryRunner struct {
	binding atomic.Pointer[runnerBinding]

	last atomic.Pointer[session.RunHandle]

	// preempt, when non-nil, stops any in-flight result-tab stream before
	// a new session op acquires the per-session queue lock. Set once at
	// wire time via SetPreempter; nil in unit tests that don't exercise
	// preemption.
	preempt func()
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
// connectInvoker once the SQLSession is ready (dbsavvy-66p.16).
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
// / Explain to stop any in-flight stream before the new session op locks
// the per-session queue (last-wins). Set once at wire time; the hook is
// stored on the runner itself so it survives Bind / Unbind. fn may be
// nil to clear the hook. Safe to call on a nil receiver.
func (r *QueryRunner) SetPreempter(fn func()) {
	if r == nil {
		return
	}
	r.preempt = fn
}

// preemptInFlight invokes the preempt hook when one is installed. Nil-safe.
func (r *QueryRunner) preemptInFlight() {
	if r == nil || r.preempt == nil {
		return
	}
	r.preempt()
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
		if _, err := b.sess.Execute(session.WithoutLogging(ctx), models.Query{SQL: "BEGIN"}); err != nil {
			return nil, err
		}
	}
	rh, err := b.sess.Stream(ctx, models.Query{SQL: sql, DefaultSchema: opts.DefaultSchema})
	if err != nil {
		return nil, err
	}
	r.last.Store(rh)
	return rh, nil
}

// RunQuery streams q (SQL + bound Args) via the SQLSession queue, mirroring
// Run but allowing parameter placeholders ($1, $2, ...) to be bound at the
// driver. The returned RunHandle is stashed for Cancel(). Used by the
// FKForwardHelper (dbsavvy-bwq.16) to issue the parameterized parent-table
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
	r.last.Store(rh)
	return rh, nil
}

// Explain delegates to SQLSession.Explain. When analyze is true and no
// transaction is currently open the call is wrapped in BEGIN/ROLLBACK
// so a side-effecting ANALYZE never auto-commits (§D14). When a
// transaction is already open the wrap is skipped — the caller's tx
// retains control over commit/rollback.
func (r *QueryRunner) Explain(ctx context.Context, sql string, analyze bool) (models.Plan, error) {
	r.preemptInFlight()
	b := r.load()
	if b == nil || b.sess == nil {
		return models.Plan{}, ErrNoSession
	}
	if !analyze || b.sess.InTransaction() {
		return b.sess.Explain(ctx, models.Query{SQL: sql}, analyze)
	}

	if _, err := b.sess.Execute(session.WithoutLogging(ctx), models.Query{SQL: "BEGIN"}); err != nil {
		return models.Plan{}, err
	}
	plan, explainErr := b.sess.Explain(ctx, models.Query{SQL: sql}, analyze)
	// Always issue ROLLBACK even if Explain errored — the BEGIN would
	// otherwise leak. The rollback error is swallowed because the
	// user-visible failure is the Explain error.
	_, _ = b.sess.Execute(session.WithoutLogging(ctx), models.Query{SQL: "ROLLBACK"})
	return plan, explainErr
}

// Cancel asks the SQLSession to cancel the last launched RunHandle.
// Returns nil when no run has been launched or when the driver lacks
// live-cancel support (the controller already gates <leader>x via
// DisabledReasonStatic; Cancel remains safe to call regardless).
func (r *QueryRunner) Cancel() error {
	b := r.load()
	if b == nil || b.sess == nil {
		return nil
	}
	if !b.caps.HasLiveCancel {
		return nil
	}
	rh := r.last.Load()
	if rh == nil {
		return nil
	}
	return b.sess.Cancel(rh.QueryID())
}
