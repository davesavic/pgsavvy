package data

import (
	"context"
	"errors"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/session"
)

// fakeTransaction is a minimal drivers.Transaction stub for tests.
type fakeTransaction struct {
	committed  bool
	rolledBack bool
}

func (t *fakeTransaction) Commit(_ context.Context) error               { t.committed = true; return nil }
func (t *fakeTransaction) Rollback(_ context.Context) error             { t.rolledBack = true; return nil }
func (t *fakeTransaction) Savepoint(_ context.Context, _ string) error  { return nil }
func (t *fakeTransaction) Release(_ context.Context, _ string) error    { return nil }
func (t *fakeTransaction) RollbackTo(_ context.Context, _ string) error { return nil }
func (t *fakeTransaction) Savepoints() []string                         { return nil }
func (t *fakeTransaction) Status() models.TxStatus                      { return models.TxActive }
func (t *fakeTransaction) ObserveError(_ error)                         {}
func (t *fakeTransaction) StatementCount() int                          { return 0 }

// fakeRunnerSession records every call and returns canned responses. It does
// NOT produce a real *session.RunHandle — Run / Stream tests rely on a
// nil RunHandle being acceptable up to QueryRunner.last.Store.
type fakeRunnerSession struct {
	execCalls    []models.Query
	streamCalls  []models.Query
	explainCalls []explainCall
	cancelCalls  []models.QueryID
	beginCalls   int

	streamErr  error
	explainErr error
	executeErr error
	cancelErr  error

	inTx   bool
	lastTx *fakeTransaction

	streamRH *session.RunHandle

	preemptMarked int
}

type explainCall struct {
	Q       models.Query
	Analyze bool
}

func (f *fakeRunnerSession) Execute(_ context.Context, q models.Query) (models.Result, error) {
	f.execCalls = append(f.execCalls, q)
	return models.Result{}, f.executeErr
}

func (f *fakeRunnerSession) Stream(_ context.Context, q models.Query) (*session.RunHandle, error) {
	f.streamCalls = append(f.streamCalls, q)
	if f.streamErr != nil {
		return nil, f.streamErr
	}
	return f.streamRH, nil
}

func (f *fakeRunnerSession) Explain(_ context.Context, q models.Query, analyze bool) (models.Plan, error) {
	f.explainCalls = append(f.explainCalls, explainCall{Q: q, Analyze: analyze})
	return models.Plan{RawText: "fake plan"}, f.explainErr
}

func (f *fakeRunnerSession) Begin(_ context.Context, _ models.TxOptions) (drivers.Transaction, error) {
	f.beginCalls++
	f.inTx = true
	f.lastTx = &fakeTransaction{}
	return f.lastTx, nil
}

func (f *fakeRunnerSession) InTransaction() bool { return f.inTx }

func (f *fakeRunnerSession) CurrentTransaction() drivers.Transaction {
	if f.lastTx == nil {
		return nil
	}
	return f.lastTx
}

func (f *fakeRunnerSession) Cancel(qid models.QueryID) error {
	f.cancelCalls = append(f.cancelCalls, qid)
	return f.cancelErr
}

func (f *fakeRunnerSession) SetDisconnected(_ bool) {}
func (f *fakeRunnerSession) IsDisconnected() bool   { return false }
func (f *fakeRunnerSession) MarkPreemptPending()    { f.preemptMarked++ }

func TestQueryRunnerRunDispatchesStream(t *testing.T) {
	fs := &fakeRunnerSession{}
	r := NewQueryRunner(fs, drivers.Capabilities{})

	_, err := r.Run(context.Background(), "SELECT 1", RunOptions{})
	if err != nil {
		t.Fatalf("Run returned err = %v", err)
	}
	if len(fs.streamCalls) != 1 || fs.streamCalls[0].SQL != "SELECT 1" {
		t.Fatalf("Stream calls = %#v, want one [SELECT 1]", fs.streamCalls)
	}
	if len(fs.execCalls) != 0 {
		t.Fatalf("unexpected Execute calls on a plain Run: %#v", fs.execCalls)
	}
}

func TestQueryRunnerRunForwardsDefaultSchema(t *testing.T) {
	fs := &fakeRunnerSession{}
	r := NewQueryRunner(fs, drivers.Capabilities{})

	_, err := r.Run(context.Background(), "SELECT 1", RunOptions{DefaultSchema: "sales"})
	if err != nil {
		t.Fatalf("Run returned err = %v", err)
	}
	if len(fs.streamCalls) != 1 || fs.streamCalls[0].DefaultSchema != "sales" {
		t.Fatalf("Stream calls = %#v, want one with DefaultSchema=sales", fs.streamCalls)
	}
}

func TestQueryRunnerRunWithNewTxPrependsBegin(t *testing.T) {
	fs := &fakeRunnerSession{}
	r := NewQueryRunner(fs, drivers.Capabilities{})

	_, err := r.Run(context.Background(), "SELECT 1", RunOptions{NewTx: true})
	if err != nil {
		t.Fatalf("Run returned err = %v", err)
	}
	if fs.beginCalls != 1 {
		t.Fatalf("Begin calls = %d, want 1", fs.beginCalls)
	}
	if len(fs.streamCalls) != 1 || fs.streamCalls[0].SQL != "SELECT 1" {
		t.Fatalf("Stream calls = %#v", fs.streamCalls)
	}
}

func TestQueryRunnerRunNoSession(t *testing.T) {
	r := NewQueryRunner(nil, drivers.Capabilities{})
	_, err := r.Run(context.Background(), "SELECT 1", RunOptions{})
	if !errors.Is(err, ErrNoSession) {
		t.Fatalf("Run no-session err = %v, want ErrNoSession", err)
	}
}

func TestQueryRunnerRunPropagatesStreamError(t *testing.T) {
	want := errors.New("driver boom")
	fs := &fakeRunnerSession{streamErr: want}
	r := NewQueryRunner(fs, drivers.Capabilities{})

	_, err := r.Run(context.Background(), "SELECT 1", RunOptions{})
	if !errors.Is(err, want) {
		t.Fatalf("Run err = %v, want %v", err, want)
	}
}

func TestQueryRunnerExplainPlainPath(t *testing.T) {
	fs := &fakeRunnerSession{}
	r := NewQueryRunner(fs, drivers.Capabilities{})

	plan, err := r.Explain(context.Background(), "SELECT 1", false, "")
	if err != nil {
		t.Fatalf("Explain err = %v", err)
	}
	if plan.RawText != "fake plan" {
		t.Fatalf("plan.RawText = %q, want fake plan", plan.RawText)
	}
	if fs.beginCalls != 0 {
		t.Fatalf("plain Explain should not issue BEGIN; beginCalls = %d", fs.beginCalls)
	}
	if len(fs.explainCalls) != 1 || fs.explainCalls[0].Analyze {
		t.Fatalf("explainCalls = %#v, want one with Analyze=false", fs.explainCalls)
	}
}

func TestQueryRunnerExplainAnalyzeWrapsInBeginRollback(t *testing.T) {
	fs := &fakeRunnerSession{} // inTx == false
	r := NewQueryRunner(fs, drivers.Capabilities{})

	plan, err := r.Explain(context.Background(), "INSERT INTO t VALUES (1)", true, "")
	if err != nil {
		t.Fatalf("Explain err = %v", err)
	}
	if plan.RawText != "fake plan" {
		t.Fatalf("plan.RawText = %q", plan.RawText)
	}
	if fs.beginCalls != 1 {
		t.Fatalf("beginCalls = %d, want 1", fs.beginCalls)
	}
	if fs.lastTx == nil || !fs.lastTx.rolledBack {
		t.Fatal("Explain ANALYZE must rollback the transaction")
	}
	if len(fs.explainCalls) != 1 || !fs.explainCalls[0].Analyze {
		t.Fatalf("explainCalls = %#v", fs.explainCalls)
	}
}

func TestQueryRunnerExplainAnalyzeInsideTxSkipsWrap(t *testing.T) {
	fs := &fakeRunnerSession{inTx: true}
	r := NewQueryRunner(fs, drivers.Capabilities{})

	if _, err := r.Explain(context.Background(), "INSERT INTO t VALUES (1)", true, ""); err != nil {
		t.Fatalf("Explain err = %v", err)
	}
	if fs.beginCalls != 0 {
		t.Fatalf("inside-tx Explain should not Begin; beginCalls = %d", fs.beginCalls)
	}
	if len(fs.explainCalls) != 1 || !fs.explainCalls[0].Analyze {
		t.Fatalf("explainCalls = %#v", fs.explainCalls)
	}
}

func TestQueryRunnerExplainAnalyzeStillRollsBackOnExplainError(t *testing.T) {
	boom := errors.New("explain boom")
	fs := &fakeRunnerSession{explainErr: boom}
	r := NewQueryRunner(fs, drivers.Capabilities{})

	_, err := r.Explain(context.Background(), "INSERT INTO t VALUES (1)", true, "")
	if !errors.Is(err, boom) {
		t.Fatalf("Explain err = %v, want %v", err, boom)
	}
	// ROLLBACK must still issue so we don't leak the BEGIN.
	if fs.beginCalls != 1 {
		t.Fatalf("beginCalls = %d, want 1", fs.beginCalls)
	}
	if fs.lastTx == nil || !fs.lastTx.rolledBack {
		t.Fatal("Explain ANALYZE must rollback even on explain error")
	}
}

func TestQueryRunnerCancelNoOpWhenNoLiveCancel(t *testing.T) {
	fs := &fakeRunnerSession{}
	r := NewQueryRunner(fs, drivers.Capabilities{HasLiveCancel: false})

	// Even with a stashed handle, Cancel must short-circuit on caps.
	if err := r.Cancel(); err != nil {
		t.Fatalf("Cancel no-live-cancel err = %v", err)
	}
	if len(fs.cancelCalls) != 0 {
		t.Fatalf("cancelCalls = %#v, want none", fs.cancelCalls)
	}
}

func TestQueryRunnerCancelNoOpWhenNoLastHandle(t *testing.T) {
	fs := &fakeRunnerSession{}
	r := NewQueryRunner(fs, drivers.Capabilities{HasLiveCancel: true})

	if err := r.Cancel(); err != nil {
		t.Fatalf("Cancel no-handle err = %v", err)
	}
	if len(fs.cancelCalls) != 0 {
		t.Fatalf("cancelCalls = %#v, want none", fs.cancelCalls)
	}
}

func TestQueryRunnerCapabilitiesEcho(t *testing.T) {
	want := drivers.Capabilities{HasLiveCancel: true, HasNotice: true, MaxIdentifierLen: 63}
	r := NewQueryRunner(&fakeRunnerSession{}, want)
	if got := r.Capabilities(); got != want {
		t.Fatalf("Capabilities() = %+v, want %+v", got, want)
	}
}

func TestQueryRunnerHasSession(t *testing.T) {
	if NewQueryRunner(nil, drivers.Capabilities{}).HasSession() {
		t.Fatal("HasSession nil-session = true, want false")
	}
	if !NewQueryRunner(&fakeRunnerSession{}, drivers.Capabilities{}).HasSession() {
		t.Fatal("HasSession with fake = false, want true")
	}
}

func TestNewQueryRunnerForSessionNilSafe(t *testing.T) {
	r := NewQueryRunnerForSession(nil, drivers.Capabilities{HasLiveCancel: true})
	if r.HasSession() {
		t.Fatal("HasSession after nil-session bootstrap = true, want false")
	}
	if r.Capabilities().HasLiveCancel != true {
		t.Fatal("caps were lost across nil-session bootstrap")
	}
}

// countingRunnerSession totals every session call so a preempt hook can
// witness how many session ops had run before it fired.
type countingRunnerSession struct {
	fakeRunnerSession
}

func (c *countingRunnerSession) calls() int {
	return len(c.execCalls) + len(c.streamCalls) + len(c.explainCalls)
}

// TestPreemptInFlightFiresBeforeSessionOp proves the chokepoint contract:
// Run, RunQuery, and Explain each invoke the preempt hook BEFORE touching
// the session, so a parked >200-row stream is stopped before the new op
// locks the per-session queue (dbsavvy-lxn.1).
func TestPreemptInFlightFiresBeforeSessionOp(t *testing.T) {
	cases := []struct {
		name   string
		invoke func(r *QueryRunner) error
	}{
		{
			name: "Run",
			invoke: func(r *QueryRunner) error {
				_, err := r.Run(context.Background(), "SELECT 1", RunOptions{})
				return err
			},
		},
		{
			name: "RunQuery",
			invoke: func(r *QueryRunner) error {
				_, err := r.RunQuery(context.Background(), models.Query{SQL: "SELECT 1"})
				return err
			},
		},
		{
			name: "Explain",
			invoke: func(r *QueryRunner) error {
				// analyze=false hits the early-return branch; the preempt
				// must still fire before the session Explain.
				_, err := r.Explain(context.Background(), "SELECT 1", false, "")
				return err
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := &countingRunnerSession{}
			r := NewQueryRunner(cs, drivers.Capabilities{})

			preemptCalls := 0
			callsAtPreempt := -1
			r.SetPreempter(func() bool {
				preemptCalls++
				callsAtPreempt = cs.calls()
				return false
			})

			if err := tc.invoke(r); err != nil {
				t.Fatalf("%s err = %v", tc.name, err)
			}
			if preemptCalls != 1 {
				t.Fatalf("preempt fired %d times, want exactly 1", preemptCalls)
			}
			if callsAtPreempt != 0 {
				t.Fatalf("preempt saw %d prior session calls, want 0 (must fire before any session op)", callsAtPreempt)
			}
		})
	}
}

// TestPreemptExpiryMarksSessionFence is the gr7e.2/AD4 seam guard: when the
// preempt hook reports expiry (worker still live, streamMu held), the
// QueryRunner — the one layer holding the session — must mark the session
// preemptPending so the subsequent op fails fast instead of deadlocking. A
// non-expiring preempt must NOT fence.
func TestPreemptExpiryMarksSessionFence(t *testing.T) {
	expired := &fakeRunnerSession{}
	r := NewQueryRunner(expired, drivers.Capabilities{})
	r.SetPreempter(func() bool { return true }) // simulate bound-expiry
	if _, err := r.Run(context.Background(), "SELECT 1", RunOptions{}); err != nil {
		t.Fatalf("Run err = %v", err)
	}
	if expired.preemptMarked != 1 {
		t.Fatalf("expiry: MarkPreemptPending called %d times, want 1", expired.preemptMarked)
	}

	ok := &fakeRunnerSession{}
	r2 := NewQueryRunner(ok, drivers.Capabilities{})
	r2.SetPreempter(func() bool { return false }) // no expiry
	if _, err := r2.Run(context.Background(), "SELECT 1", RunOptions{}); err != nil {
		t.Fatalf("Run err = %v", err)
	}
	if ok.preemptMarked != 0 {
		t.Fatalf("no expiry: MarkPreemptPending called %d times, want 0", ok.preemptMarked)
	}
}

// TestPreemptSurvivesUnbindRebind is the highest-risk regression guard:
// the preempt hook lives on *QueryRunner, NOT on runnerBinding, so an
// atomic Bind / Unbind swap on reconnect must not silently drop it and
// reintroduce the UI freeze (dbsavvy-lxn.1). Bind is exercised via
// SetPreempter + NewQueryRunner here; the production swap is identical.
func TestPreemptSurvivesUnbindRebind(t *testing.T) {
	r := NewQueryRunner(&fakeRunnerSession{}, drivers.Capabilities{})

	fired := 0
	r.SetPreempter(func() bool { fired++; return false })

	// Swap the binding twice (Unbind then re-Bind with a fresh session),
	// mirroring a disconnect / reconnect cycle.
	r.Unbind()
	r.binding.Store(&runnerBinding{sess: &fakeRunnerSession{}})

	if _, err := r.Run(context.Background(), "SELECT 1", RunOptions{}); err != nil {
		t.Fatalf("Run after rebind err = %v", err)
	}
	if fired != 1 {
		t.Fatalf("preempt fired %d times after Unbind/rebind, want 1 (hook must survive the binding swap)", fired)
	}
}
