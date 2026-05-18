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
// safe; they queue inside SQLSession.
type QueryRunner struct {
	sess RunnerSession
	caps drivers.Capabilities

	last atomic.Pointer[session.RunHandle]
}

// NewQueryRunner builds a QueryRunner bound to sess. caps captures the
// driver's capability flags at construction time — the controller
// reads QueryRunner.Capabilities().HasLiveCancel at RegisterActions
// time so the <leader>x DisabledReasonStatic stays accurate without a
// re-registration on driver swap (driver swap goes through reconnect →
// bootstrap → fresh QueryRunner).
//
// sess may be nil; every method nil-checks and returns ErrNoSession
// (or, for Cancel, silently no-ops).
func NewQueryRunner(sess RunnerSession, caps drivers.Capabilities) *QueryRunner {
	return &QueryRunner{sess: sess, caps: caps}
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

// Capabilities returns the driver capabilities captured at construction.
func (r *QueryRunner) Capabilities() drivers.Capabilities { return r.caps }

// HasSession reports whether a SQLSession is wired. Tests / the
// controller use this to short-circuit before invoking a binding's
// handler so users see a "no connection" toast instead of an error.
func (r *QueryRunner) HasSession() bool { return r != nil && r.sess != nil }

// Run streams sql via the SQLSession queue. When opts.NewTx is true a
// BEGIN is issued via Execute immediately before the Stream; both
// operations queue on the SQLSession serializer (Begin / Execute share
// the queue mutex with Stream).
//
// The returned RunHandle is also stashed for Cancel(). Callers should
// hand it to ResultTabsHelper.OpenResultTab; the tab owns the row
// drain afterwards.
func (r *QueryRunner) Run(ctx context.Context, sql string, opts RunOptions) (*session.RunHandle, error) {
	if r == nil || r.sess == nil {
		return nil, ErrNoSession
	}
	if opts.NewTx {
		if _, err := r.sess.Execute(session.WithoutLogging(ctx), models.Query{SQL: "BEGIN"}); err != nil {
			return nil, err
		}
	}
	rh, err := r.sess.Stream(ctx, models.Query{SQL: sql})
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
	if r == nil || r.sess == nil {
		return models.Plan{}, ErrNoSession
	}
	if !analyze || r.sess.InTransaction() {
		return r.sess.Explain(ctx, models.Query{SQL: sql}, analyze)
	}

	if _, err := r.sess.Execute(session.WithoutLogging(ctx), models.Query{SQL: "BEGIN"}); err != nil {
		return models.Plan{}, err
	}
	plan, explainErr := r.sess.Explain(ctx, models.Query{SQL: sql}, analyze)
	// Always issue ROLLBACK even if Explain errored — the BEGIN would
	// otherwise leak. The rollback error is swallowed because the
	// user-visible failure is the Explain error.
	_, _ = r.sess.Execute(session.WithoutLogging(ctx), models.Query{SQL: "ROLLBACK"})
	return plan, explainErr
}

// Cancel asks the SQLSession to cancel the last launched RunHandle.
// Returns nil when no run has been launched or when the driver lacks
// live-cancel support (the controller already gates <leader>x via
// DisabledReasonStatic; Cancel remains safe to call regardless).
func (r *QueryRunner) Cancel() error {
	if r == nil || r.sess == nil {
		return nil
	}
	if !r.caps.HasLiveCancel {
		return nil
	}
	rh := r.last.Load()
	if rh == nil {
		return nil
	}
	return r.sess.Cancel(rh.QueryID())
}
