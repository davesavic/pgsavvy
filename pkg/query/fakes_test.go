package query

import (
	"context"
	"sync"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
)

// fakeExecutor records every dispatch and lets tests stage results.
type fakeExecutor struct {
	mu sync.Mutex

	executeCalls []executeCall
	streamCalls  []streamCall
	explainCalls []explainCall

	executeRes models.Result
	executeErr error

	streamHandle *session.RunHandle
	streamErr    error

	explainPlan models.Plan
	explainErr  error
}

type executeCall struct {
	Query       models.Query
	Suppressed  bool
	HasDeadline bool
}

type streamCall struct {
	Query      models.Query
	Suppressed bool
}

type explainCall struct {
	Query      models.Query
	Analyze    bool
	Suppressed bool
}

func (f *fakeExecutor) Execute(ctx context.Context, q models.Query) (models.Result, error) {
	f.mu.Lock()
	f.executeCalls = append(f.executeCalls, executeCall{
		Query:      q,
		Suppressed: session.LoggingSuppressed(ctx),
	})
	f.mu.Unlock()
	return f.executeRes, f.executeErr
}

func (f *fakeExecutor) Stream(ctx context.Context, q models.Query) (*session.RunHandle, error) {
	f.mu.Lock()
	f.streamCalls = append(f.streamCalls, streamCall{
		Query:      q,
		Suppressed: session.LoggingSuppressed(ctx),
	})
	f.mu.Unlock()
	return f.streamHandle, f.streamErr
}

func (f *fakeExecutor) Explain(ctx context.Context, q models.Query, analyze bool) (models.Plan, error) {
	f.mu.Lock()
	f.explainCalls = append(f.explainCalls, explainCall{
		Query:      q,
		Analyze:    analyze,
		Suppressed: session.LoggingSuppressed(ctx),
	})
	f.mu.Unlock()
	return f.explainPlan, f.explainErr
}

func (f *fakeExecutor) lastExecute() executeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.executeCalls[len(f.executeCalls)-1]
}

func (f *fakeExecutor) lastStream() streamCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.streamCalls[len(f.streamCalls)-1]
}

func (f *fakeExecutor) lastExplain() explainCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.explainCalls[len(f.explainCalls)-1]
}

// fakeTx is a no-op drivers.Transaction used to verify WithTx records the
// value (no routing in v1 — the field is set but dispatch ignores it).
type fakeTx struct{}

func (fakeTx) Commit(context.Context) error             { return nil }
func (fakeTx) Rollback(context.Context) error           { return nil }
func (fakeTx) Savepoint(context.Context, string) error  { return nil }
func (fakeTx) Release(context.Context, string) error    { return nil }
func (fakeTx) RollbackTo(context.Context, string) error { return nil }
func (fakeTx) Savepoints() []string                     { return nil }
func (fakeTx) Status() models.TxStatus                  { return models.TxActive }
func (fakeTx) ObserveError(error)                       {}
func (fakeTx) StatementCount() int                      { return 0 }

var (
	_ executor            = (*fakeExecutor)(nil)
	_ drivers.Transaction = fakeTx{}
)
