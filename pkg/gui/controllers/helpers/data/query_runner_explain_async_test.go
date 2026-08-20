package data

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/logs"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
)

// sequencedSession records Begin / Explain / Rollback / preempt events
// into one ordered slice so async-launch tests can assert the exact
// interleaving the single-flight queue produces. It satisfies
// RunnerSession; Stream is never reached by the ExplainAsync path.
type sequencedSession struct {
	mu  sync.Mutex
	seq []string

	inTx          bool
	lastTx        *seqTx
	txRollbackErr error
}

func (s *sequencedSession) add(ev string) {
	s.mu.Lock()
	s.seq = append(s.seq, ev)
	s.mu.Unlock()
}

func (s *sequencedSession) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.seq))
	copy(out, s.seq)
	return out
}

func (s *sequencedSession) Execute(context.Context, models.Query) (models.Result, error) {
	return models.Result{}, nil
}

func (s *sequencedSession) Stream(context.Context, models.Query) (*session.RunHandle, error) {
	return nil, nil
}

func (s *sequencedSession) Explain(_ context.Context, q models.Query, _ bool) (models.Plan, error) {
	s.add("explain:" + q.SQL)
	return models.Plan{RawText: "fake plan"}, nil
}

func (s *sequencedSession) Begin(context.Context, models.TxOptions) (drivers.Transaction, error) {
	s.mu.Lock()
	s.seq = append(s.seq, "begin")
	s.inTx = true
	s.lastTx = &seqTx{s: s, rollbackErr: s.txRollbackErr}
	tx := s.lastTx
	s.mu.Unlock()
	return tx, nil
}

func (s *sequencedSession) InTransaction() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inTx
}

func (s *sequencedSession) CurrentTransaction() drivers.Transaction {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastTx
}

func (s *sequencedSession) LiveTxStatus() (models.TxStatus, []string) { return "", nil }

func (s *sequencedSession) Cancel(models.QueryID) error { return nil }

func (s *sequencedSession) SetDisconnected(bool) {}

func (s *sequencedSession) IsDisconnected() bool { return false }

func (s *sequencedSession) MarkPreemptPending() {}

// seqTx is the sequencedSession's transaction; its Rollback records the
// event and may inject a failure to exercise the warn path. It records
// commit/rollback on itself so tests can assert which one the quit path
// actually performed on a runner-owned wrap tx.
type seqTx struct {
	s           *sequencedSession
	rollbackErr error

	committed  atomic.Bool
	rolledBack atomic.Bool
}

func (t *seqTx) Commit(context.Context) error {
	t.committed.Store(true)
	return nil
}

func (t *seqTx) Rollback(context.Context) error {
	t.rolledBack.Store(true)
	t.s.mu.Lock()
	t.s.seq = append(t.s.seq, "rollback")
	t.s.inTx = false
	t.s.lastTx = nil
	t.s.mu.Unlock()
	return t.rollbackErr
}

func (t *seqTx) Savepoint(context.Context, string) error  { return nil }
func (t *seqTx) Release(context.Context, string) error    { return nil }
func (t *seqTx) RollbackTo(context.Context, string) error { return nil }
func (t *seqTx) Savepoints() []string                     { return nil }
func (t *seqTx) Status() models.TxStatus                  { return models.TxActive }
func (t *seqTx) ObserveError(error)                       {}
func (t *seqTx) StatementCount() int                      { return 0 }

// TestExplainAsyncAnalyzeSerializesOnQueue proves the single-flight
// queue serializes two rapid ExplainAsync (analyze=true, no open tx):
// the second op's BEGIN can never land inside the first op's
// BEGIN..ROLLBACK span. The second enqueue's last-wins cancellation may
// abandon the first launch, but the first op (already past the queued
// fence once its Begin is recorded) still completes its wrap on the
// session — so the recorded sequence is deterministic regardless of the
// enqueue race.
func TestExplainAsyncAnalyzeSerializesOnQueue(t *testing.T) {
	fs := &sequencedSession{}
	r := NewQueryRunner(fs, drivers.Capabilities{})

	ackA := make(chan struct{})
	ackB := make(chan struct{})
	r.ExplainAsync(context.Background(), "SELECT A", true, "", func(models.Plan, error) { close(ackA) })

	// Wait until the first op is past the queued-cancel fence (Begin
	// recorded) before enqueueing the second, so both ops actually run.
	deadline := time.After(2 * time.Second)
	for len(fs.snapshot()) == 0 {
		select {
		case <-deadline:
			t.Fatal("first ExplainAsync op never started")
		case <-time.After(time.Millisecond):
		}
	}
	r.ExplainAsync(context.Background(), "SELECT B", true, "", func(models.Plan, error) { close(ackB) })

	for _, ack := range []chan struct{}{ackA, ackB} {
		select {
		case <-ack:
		case <-time.After(2 * time.Second):
			t.Fatal("ExplainAsync ack never arrived")
		}
	}

	// The queue guarantees strict ordering: A's full wrap precedes B's.
	want := []string{"begin", "explain:SELECT A", "rollback", "begin", "explain:SELECT B", "rollback"}
	if got := fs.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("sequence = %v, want %v (Begin_B must serialize after Rollback_A)", got, want)
	}
}

// TestExplainAsyncNoSession surfaces ErrNoSession through the ack when no
// session is wired at op time (the controller's HasSession guard normally
// prevents this; the queue path still fails closed if a session is lost
// between enqueue and the launcher running the op).
func TestExplainAsyncNoSession(t *testing.T) {
	r := NewQueryRunner(nil, drivers.Capabilities{})

	done := make(chan struct{})
	var gotErr error
	r.ExplainAsync(context.Background(), "SELECT 1", false, "", func(_ models.Plan, err error) {
		gotErr = err
		close(done)
	})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ExplainAsync ack never arrived")
	}
	if !errors.Is(gotErr, ErrNoSession) {
		t.Fatalf("ack err = %v, want ErrNoSession", gotErr)
	}
}

// TestExplainAsyncPlainPathNoWrap proves the analyze=false op skips the
// BEGIN/ROLLBACK wrap entirely.
func TestExplainAsyncPlainPathNoWrap(t *testing.T) {
	fs := &sequencedSession{}
	r := NewQueryRunner(fs, drivers.Capabilities{})

	done := make(chan struct{})
	var gotPlan models.Plan
	var gotErr error
	r.ExplainAsync(context.Background(), "SELECT 1", false, "", func(plan models.Plan, err error) {
		gotPlan, gotErr = plan, err
		close(done)
	})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ExplainAsync ack never arrived")
	}
	if gotErr != nil {
		t.Fatalf("ack err = %v, want nil", gotErr)
	}
	if gotPlan.RawText != "fake plan" {
		t.Fatalf("plan.RawText = %q, want fake plan", gotPlan.RawText)
	}
	if want := []string{"explain:SELECT 1"}; !reflect.DeepEqual(fs.snapshot(), want) {
		t.Fatalf("sequence = %v, want %v (no wrap on plain EXPLAIN)", fs.snapshot(), want)
	}
}

// TestExplainAsyncAnalyzeWrapsInBeginRollback proves the analyze=true op
// outside a transaction issues BEGIN -> EXPLAIN -> ROLLBACK and surfaces
// the plan.
func TestExplainAsyncAnalyzeWrapsInBeginRollback(t *testing.T) {
	fs := &sequencedSession{}
	r := NewQueryRunner(fs, drivers.Capabilities{})

	done := make(chan struct{})
	var gotPlan models.Plan
	var gotErr error
	r.ExplainAsync(context.Background(), "INSERT INTO t VALUES (1)", true, "", func(plan models.Plan, err error) {
		gotPlan, gotErr = plan, err
		close(done)
	})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ExplainAsync ack never arrived")
	}
	if gotErr != nil {
		t.Fatalf("ack err = %v, want nil", gotErr)
	}
	if gotPlan.RawText != "fake plan" {
		t.Fatalf("plan.RawText = %q, want fake plan", gotPlan.RawText)
	}
	want := []string{"begin", "explain:INSERT INTO t VALUES (1)", "rollback"}
	if got := fs.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("sequence = %v, want %v", got, want)
	}
}

// TestExplainAsyncInsideTxSkipsWrap proves the InTransaction gate is read
// FRESH inside the queued op: an already-open tx skips the auto-wrap and
// keeps control of commit/rollback with the caller.
func TestExplainAsyncInsideTxSkipsWrap(t *testing.T) {
	fs := &sequencedSession{inTx: true}
	r := NewQueryRunner(fs, drivers.Capabilities{})

	done := make(chan struct{})
	r.ExplainAsync(context.Background(), "INSERT INTO t VALUES (1)", true, "", func(models.Plan, error) {
		close(done)
	})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ExplainAsync ack never arrived")
	}
	if want := []string{"explain:INSERT INTO t VALUES (1)"}; !reflect.DeepEqual(fs.snapshot(), want) {
		t.Fatalf("sequence = %v, want %v (no wrap inside an open tx)", fs.snapshot(), want)
	}
}

// TestExplainAsyncRollbackFailureWarns covers the "no silent swallow" AC:
// a failed ROLLBACK after EXPLAIN ANALYZE is warn-logged (logs.Event
// hard-codes Debug, so the WARN is emitted directly) while the user-visible
// result stays the Explain outcome.
func TestExplainAsyncRollbackFailureWarns(t *testing.T) {
	rollbackBoom := errors.New("rollback boom")
	fs := &sequencedSession{txRollbackErr: rollbackBoom}
	handler := logs.NewRecordingHandler()
	r := NewQueryRunner(fs, drivers.Capabilities{})
	r.SetLogger(slog.New(handler))

	done := make(chan struct{})
	var gotErr error
	r.ExplainAsync(context.Background(), "SELECT 1", true, "", func(_ models.Plan, err error) {
		gotErr = err
		close(done)
	})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ExplainAsync ack never arrived")
	}
	// The explain succeeded; the rollback failure must not surface to the
	// caller as the op's error.
	if gotErr != nil {
		t.Fatalf("ack err = %v, want nil (rollback failure is warn-logged, not surfaced)", gotErr)
	}
	recs := handler.Records()
	if len(recs) != 1 {
		t.Fatalf("warn records = %d, want exactly 1", len(recs))
	}
	if recs[0].Level != slog.LevelWarn {
		t.Fatalf("record level = %v, want Warn", recs[0].Level)
	}
	if recs[0].Message != "explain_rollback_failed" {
		t.Fatalf("record message = %q, want explain_rollback_failed", recs[0].Message)
	}
	var errAttr string
	recs[0].Attrs(func(a slog.Attr) bool {
		if a.Key == "err" {
			errAttr = a.Value.String()
		}
		return true
	})
	if errAttr == "" {
		t.Fatal("warn record missing the rollback err attr")
	}

	// The sync Explain path must warn-log the same failure.
	fs2 := &sequencedSession{txRollbackErr: rollbackBoom}
	handler2 := logs.NewRecordingHandler()
	r2 := NewQueryRunner(fs2, drivers.Capabilities{})
	r2.SetLogger(slog.New(handler2))
	if _, err := r2.Explain(context.Background(), "SELECT 1", true, ""); err != nil {
		t.Fatalf("sync Explain err = %v, want nil", err)
	}
	recs2 := handler2.Records()
	if len(recs2) != 1 || recs2[0].Level != slog.LevelWarn {
		t.Fatalf("sync records = %+v, want exactly 1 warn", recs2)
	}
}

// TestExplainAsyncPreemptFiresBeforeOp proves the preempt-first ordering
// is preserved through the queue: the launcher runs preemptBeforeLaunch
// BEFORE the Explain op touches the session, exactly as for a Run.
func TestExplainAsyncPreemptFiresBeforeOp(t *testing.T) {
	fs := &sequencedSession{}
	r := NewQueryRunner(fs, drivers.Capabilities{})
	r.SetPreempter(func() bool {
		fs.add("preempt")
		return false
	})

	done := make(chan struct{})
	r.ExplainAsync(context.Background(), "SELECT 1", false, "", func(models.Plan, error) { close(done) })
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ExplainAsync ack never arrived")
	}
	want := []string{"preempt", "explain:SELECT 1"}
	if got := fs.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("sequence = %v, want %v (preempt must precede the session Explain)", got, want)
	}
}
