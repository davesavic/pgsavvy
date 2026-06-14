// Package query provides a fluent, value-immutable builder (QueryObj) that
// wraps a *session.SQLSession and dispatches Execute / Stream / Explain.
//
// Each WithX method returns a NEW *QueryObj; the receiver is left
// untouched, so a base builder can be safely reused across goroutines as a
// template. Concurrency control still lives entirely in SQLSession —
// QueryObj is a pure builder.
package query

import (
	"context"
	"errors"
	"time"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/session"
)

// ErrEmptyStatement is returned by Execute / Stream / Explain when the
// QueryObj was constructed (or derived) with an empty SQL string. New
// itself never errors — empty SQL is only rejected at dispatch.
var ErrEmptyStatement = errors.New("query: empty statement")

// executor is the subset of *session.SQLSession that QueryObj dispatches
// against. Defined as an interface so tests can supply a fake without
// needing a real driver connection. *session.SQLSession satisfies this
// interface (compile-time check below).
type executor interface {
	Execute(ctx context.Context, q models.Query) (models.Result, error)
	Stream(ctx context.Context, q models.Query) (*session.RunHandle, error)
	Explain(ctx context.Context, q models.Query, analyze bool) (models.Plan, error)
}

var _ executor = (*session.SQLSession)(nil)

// QueryObj is the fluent builder. Construct via New and chain WithX
// methods; each WithX returns a copy. The zero value is not useful — use
// New.
type QueryObj struct {
	exec    executor
	sql     string
	args    []any
	timeout time.Duration
	tx      drivers.Transaction
	dontLog bool
}

// NewQueryObj constructs a QueryObj over sess for sql. sql may be empty;
// the empty-statement check is deferred to dispatch (Execute / Stream /
// Explain) so callers can build a template and fill it in later.
//
// (Named NewQueryObj rather than New because pkg/query already exports
// New as the constructor for the History type.)
func NewQueryObj(sess *session.SQLSession, sql string) *QueryObj {
	return &QueryObj{exec: sess, sql: sql}
}

// newWith is the test seam: it lets fakes_test.go inject a fake executor
// without depending on a real *session.SQLSession.
func newWith(exec executor, sql string) *QueryObj {
	return &QueryObj{exec: exec, sql: sql}
}

// clone returns a shallow copy with args duplicated. The args slice is
// duplicated because callers can hold a reference to the underlying array
// via Bind; we don't want a later Bind on a child to mutate the parent's
// slice through aliasing.
func (q *QueryObj) clone() *QueryObj {
	cp := *q
	if q.args != nil {
		cp.args = append([]any(nil), q.args...)
	}
	return &cp
}

// Bind sets the bound arguments. Calling Bind a second time REPLACES the
// previous arguments rather than appending — the test contract.
func (q *QueryObj) Bind(args ...any) *QueryObj {
	cp := q.clone()
	if len(args) == 0 {
		cp.args = nil
	} else {
		cp.args = append([]any(nil), args...)
	}
	return cp
}

// WithTimeout sets the per-query timeout passed through to models.Query.
// A zero duration is preserved and means "no timeout" at the driver layer.
func (q *QueryObj) WithTimeout(d time.Duration) *QueryObj {
	cp := q.clone()
	cp.timeout = d
	return cp
}

// WithTx records an intent to run inside tx. v1 limitation: SQLSession
// uses the session's in-progress transaction implicitly (driver-side
// tracking), so WithTx is a no-op assertion — the value is stored but not
// re-routed at dispatch time. Passing nil is equivalent to omitting.
func (q *QueryObj) WithTx(tx drivers.Transaction) *QueryObj {
	cp := q.clone()
	cp.tx = tx
	return cp
}

// DontLog suppresses HistoryRecorder.Record for this dispatch by wrapping
// the context with session.WithoutLogging before delegating.
func (q *QueryObj) DontLog() *QueryObj {
	cp := q.clone()
	cp.dontLog = true
	return cp
}

// modelsQuery assembles the models.Query payload from the builder state.
func (q *QueryObj) modelsQuery() models.Query {
	return models.Query{SQL: q.sql, Args: q.args, Timeout: q.timeout}
}

// applyCtx wraps ctx with WithoutLogging when dontLog is set.
func (q *QueryObj) applyCtx(ctx context.Context) context.Context {
	if q.dontLog {
		return session.WithoutLogging(ctx)
	}
	return ctx
}

// Execute dispatches an Execute on the underlying SQLSession.
func (q *QueryObj) Execute(ctx context.Context) (models.Result, error) {
	if q.sql == "" {
		return models.Result{}, ErrEmptyStatement
	}
	return q.exec.Execute(q.applyCtx(ctx), q.modelsQuery())
}

// Stream dispatches a Stream and returns the RunHandle the SQLSession
// produced. The handle is passed through unwrapped.
func (q *QueryObj) Stream(ctx context.Context) (*session.RunHandle, error) {
	if q.sql == "" {
		return nil, ErrEmptyStatement
	}
	return q.exec.Stream(q.applyCtx(ctx), q.modelsQuery())
}

// Explain dispatches an Explain on the underlying SQLSession with the
// supplied analyze flag.
func (q *QueryObj) Explain(ctx context.Context, analyze bool) (models.Plan, error) {
	if q.sql == "" {
		return models.Plan{}, ErrEmptyStatement
	}
	return q.exec.Explain(q.applyCtx(ctx), q.modelsQuery(), analyze)
}
