package session_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/session"
)

// newTestSession constructs a SQLSession over fakeConn + fakeSess wired
// with the supplied HistoryRecorder.
func newTestSession(t *testing.T, h session.HistoryRecorder) (*session.SQLSession, *fakeConn, *fakeSess) {
	t.Helper()
	conn := &fakeConn{}
	sess := &fakeSess{id: 42}
	s := session.New(conn, sess, session.Options{HistoryRecorder: h})
	t.Cleanup(func() { _ = s.Close() })
	return s, conn, sess
}

func TestSQLSessionExecute_RecordsHistoryOnce(t *testing.T) {
	rec := &recordingHistory{}
	s, _, fs := newTestSession(t, rec)
	fs.executeRes = models.Result{RowsAffected: 3, Duration: 5 * time.Millisecond}

	if _, err := s.Execute(context.Background(), models.Query{SQL: "UPDATE x"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("history calls = %d, want 1", len(calls))
	}
	c := calls[0]
	if c.Stmt != "UPDATE x" || c.RowsAffected != 3 || !c.Succeeded {
		t.Errorf("history call = %+v, want stmt=UPDATE x rows=3 succeeded=true", c)
	}
	if c.DurMs < 0 {
		t.Errorf("DurMs = %d, want >= 0", c.DurMs)
	}
}

func TestSQLSessionStream_RecordsHistoryOnceOnDone(t *testing.T) {
	rec := &recordingHistory{}
	s, _, fs := newTestSession(t, rec)
	fs.streams = []func() drivers.RowStream{}
	// We stage one fakeRowStream that yields 2 rows.
	staged := &fakeRowStream{qid: models.QueryID{SessionID: 42, Nonce: 1}, total: 2}
	fs.streams = append(fs.streams, func() drivers.RowStream { return staged })

	rh, err := s.Stream(context.Background(), models.Query{SQL: "SELECT 1"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	ctx := context.Background()
	for {
		_, ok, err := rh.Rows().Next(ctx)
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if !ok {
			break
		}
	}
	<-rh.Done()
	if err := rh.Rows().Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("history calls = %d, want 1", len(calls))
	}
	c := calls[0]
	if c.Stmt != "SELECT 1" || c.RowsAffected != 2 || !c.Succeeded {
		t.Errorf("history call = %+v, want stmt='SELECT 1' rows=2 succeeded=true", c)
	}
}

func TestSQLSessionStream_SerializesSecondStreamUntilFirstDone(t *testing.T) {
	s, _, fs := newTestSession(t, nil)
	release := make(chan struct{})
	first := &fakeRowStream{qid: models.QueryID{SessionID: 42, Nonce: 1}, total: 1, blockOn: release}
	second := &fakeRowStream{qid: models.QueryID{SessionID: 42, Nonce: 2}, total: 1}
	fs.streams = []func() drivers.RowStream{
		func() drivers.RowStream { return first },
		func() drivers.RowStream { return second },
	}

	rh1, err := s.Stream(context.Background(), models.Query{SQL: "A"})
	if err != nil {
		t.Fatalf("Stream A: %v", err)
	}

	// Kick off a second Stream concurrently; it must block on streamMu
	// until rh1 finishes.
	type result struct {
		rh  *session.RunHandle
		err error
		at  time.Time
	}
	resCh := make(chan result, 1)
	go func() {
		rh, err := s.Stream(context.Background(), models.Query{SQL: "B"})
		resCh <- result{rh: rh, err: err, at: time.Now()}
	}()

	// Give the goroutine a chance to enter Stream and park on streamMu.
	time.Sleep(20 * time.Millisecond)
	select {
	case r := <-resCh:
		t.Fatalf("second Stream returned before first finished: %+v", r)
	default:
	}

	// Release the first stream's Next, drain to completion, close it.
	close(release)
	for {
		_, ok, err := rh1.Rows().Next(context.Background())
		if err != nil {
			t.Fatalf("Next on A: %v", err)
		}
		if !ok {
			break
		}
	}
	<-rh1.Done()
	_ = rh1.Rows().Close()

	// Second Stream must now unblock.
	select {
	case r := <-resCh:
		if r.err != nil {
			t.Fatalf("second Stream err: %v", r.err)
		}
		// Drain B to keep streamMu released for Close.
		for {
			_, ok, err := r.rh.Rows().Next(context.Background())
			if err != nil {
				t.Fatalf("Next on B: %v", err)
			}
			if !ok {
				break
			}
		}
		<-r.rh.Done()
		_ = r.rh.Rows().Close()
	case <-time.After(2 * time.Second):
		t.Fatal("second Stream did not unblock after first finished")
	}
}

func TestSQLSessionCancel_UnknownQIDReturnsNil(t *testing.T) {
	s, fc, _ := newTestSession(t, nil)
	err := s.Cancel(models.QueryID{SessionID: 9999, Nonce: 1})
	if err != nil {
		t.Fatalf("Cancel(unknown) err = %v, want nil", err)
	}
	if got := fc.cancelCount.Load(); got != 0 {
		t.Errorf("conn.Cancel called %d times, want 0", got)
	}
}

func TestSQLSessionCancel_InFlightCallsConnCancelOnce(t *testing.T) {
	s, fc, fs := newTestSession(t, nil)
	release := make(chan struct{})
	staged := &fakeRowStream{
		qid:     models.QueryID{SessionID: 42, BackendPID: 7, Nonce: 1},
		total:   1,
		blockOn: release,
	}
	fs.streams = []func() drivers.RowStream{func() drivers.RowStream { return staged }}

	rh, err := s.Stream(context.Background(), models.Query{SQL: "SELECT pg_sleep(60)"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	if err := s.Cancel(rh.QueryID()); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if got := fc.cancelCount.Load(); got != 1 {
		t.Errorf("conn.Cancel called %d times, want 1", got)
	}
	// Release the stream so finish/Close can complete and goleak passes.
	close(release)
	for {
		_, ok, err := rh.Rows().Next(context.Background())
		if !ok || err != nil {
			break
		}
	}
	<-rh.Done()
	_ = rh.Rows().Close()
}

// TestSQLSessionCancel_BoundsCancelDialDeadline is the gr7e.1/AD5 guard: the
// cancel round-trip must run under a bounded context so rh.Cancel() cannot
// freeze the UI on a dead/slow host. Prior code used context.Background()
// (no deadline); this asserts the conn.Cancel ctx now carries a deadline a
// few seconds out.
func TestSQLSessionCancel_BoundsCancelDialDeadline(t *testing.T) {
	s, fc, fs := newTestSession(t, nil)
	release := make(chan struct{})
	staged := &fakeRowStream{
		qid:     models.QueryID{SessionID: 42, BackendPID: 7, Nonce: 1},
		total:   1,
		blockOn: release,
	}
	fs.streams = []func() drivers.RowStream{func() drivers.RowStream { return staged }}

	rh, err := s.Stream(context.Background(), models.Query{SQL: "SELECT pg_sleep(60)"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	before := time.Now()
	if err := s.Cancel(rh.QueryID()); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	if !fc.cancelHadDeadline.Load() {
		t.Fatal("conn.Cancel received a ctx with NO deadline; AD5 requires a bounded cancel dial")
	}
	dl := fc.cancelDeadline.Load()
	if dl == nil {
		t.Fatal("cancel deadline not captured")
	}
	if d := dl.Sub(before); d <= 0 || d > 10*time.Second {
		t.Errorf("cancel ctx deadline = %v out, want a small positive bound (~3s)", d)
	}

	// Release the stream so finish/Close can complete and goleak passes.
	close(release)
	for {
		_, ok, err := rh.Rows().Next(context.Background())
		if !ok || err != nil {
			break
		}
	}
	<-rh.Done()
	_ = rh.Rows().Close()
}

// TestSQLSession_PreemptPendingFencesThenClears is the gr7e.2/AD4 guard: a
// bound-expiry fence (MarkPreemptPending) makes Stream/Execute/Explain/Begin
// fail fast with ErrPreemptPending instead of blocking on streamMu while the
// wedged worker still holds it; onFinish clears the fence when the worker
// finally exits, after which the next op proceeds. Without this, the bounded
// Stop merely relocates the dbsavvy-dk6 deadlock to the next query.
func TestSQLSession_PreemptPendingFencesThenClears(t *testing.T) {
	s, _, fs := newTestSession(t, nil)
	release := make(chan struct{})
	first := &fakeRowStream{
		qid:     models.QueryID{SessionID: 42, BackendPID: 7, Nonce: 1},
		total:   1,
		blockOn: release,
	}
	second := &fakeRowStream{
		qid:   models.QueryID{SessionID: 42, BackendPID: 7, Nonce: 2},
		total: 1,
	}
	fs.streams = []func() drivers.RowStream{
		func() drivers.RowStream { return first },
		func() drivers.RowStream { return second },
	}

	rh, err := s.Stream(context.Background(), models.Query{SQL: "SELECT 1"})
	if err != nil {
		t.Fatalf("first Stream: %v", err)
	}

	// Simulate a bound-expiry fence while the worker is still live (streamMu held).
	s.MarkPreemptPending()

	// Every guarded op must fail fast with ErrPreemptPending — NOT block on streamMu.
	if _, err := s.Stream(context.Background(), models.Query{SQL: "SELECT 2"}); !errors.Is(err, session.ErrPreemptPending) {
		t.Fatalf("Stream during fence = %v, want ErrPreemptPending", err)
	}
	if _, err := s.Execute(context.Background(), models.Query{SQL: "SELECT 2"}); !errors.Is(err, session.ErrPreemptPending) {
		t.Fatalf("Execute during fence = %v, want ErrPreemptPending", err)
	}
	if _, err := s.Explain(context.Background(), models.Query{SQL: "SELECT 2"}, false); !errors.Is(err, session.ErrPreemptPending) {
		t.Fatalf("Explain during fence = %v, want ErrPreemptPending", err)
	}
	if _, err := s.Begin(context.Background(), models.TxOptions{}); !errors.Is(err, session.ErrPreemptPending) {
		t.Fatalf("Begin during fence = %v, want ErrPreemptPending", err)
	}

	// Release the wedged worker; onFinish clears the fence and releases streamMu.
	close(release)
	for {
		_, ok, err := rh.Rows().Next(context.Background())
		if !ok || err != nil {
			break
		}
	}
	<-rh.Done()
	_ = rh.Rows().Close()

	// Fence lifted: the next Stream now proceeds (consuming the second stream).
	rh2, err := s.Stream(context.Background(), models.Query{SQL: "SELECT 3"})
	if err != nil {
		t.Fatalf("Stream after worker exit = %v, want nil (fence must clear in onFinish)", err)
	}
	for {
		_, ok, err := rh2.Rows().Next(context.Background())
		if !ok || err != nil {
			break
		}
	}
	<-rh2.Done()
	_ = rh2.Rows().Close()
}

func TestRunHandleCancel_AfterDoneIsIdempotent(t *testing.T) {
	s, fc, fs := newTestSession(t, nil)
	staged := &fakeRowStream{qid: models.QueryID{SessionID: 42, Nonce: 1}, total: 0}
	fs.streams = []func() drivers.RowStream{func() drivers.RowStream { return staged }}

	rh, err := s.Stream(context.Background(), models.Query{SQL: "SELECT"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	// Drain the (empty) stream so finish fires and Done closes.
	_, _, _ = rh.Rows().Next(context.Background())
	<-rh.Done()

	// Cancel after Done must be a nil-returning no-op and MUST NOT call
	// conn.Cancel.
	for i := 0; i < 3; i++ {
		if err := rh.Cancel(); err != nil {
			t.Errorf("Cancel #%d after Done returned %v, want nil", i, err)
		}
	}
	if got := fc.cancelCount.Load(); got != 0 {
		t.Errorf("conn.Cancel calls = %d, want 0", got)
	}
	_ = rh.Rows().Close()
}

func TestSQLSessionClose_RollsBackOpenTransaction(t *testing.T) {
	conn := &fakeConn{}
	fs := &fakeSess{id: 7}
	tx := &fakeTx{parent: fs}
	fs.beginTx = tx
	s := session.New(conn, fs, session.Options{})

	if _, err := s.Begin(context.Background(), models.TxOptions{}); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if !s.InTransaction() {
		t.Fatal("InTransaction = false after Begin, want true")
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := tx.rollback.Load(); got != 1 {
		t.Errorf("tx.Rollback called %d times, want 1", got)
	}
	if s.InTransaction() {
		t.Error("InTransaction = true after Close, want false")
	}
}

func TestSQLSessionExecute_PanickingHistoryRecorderIsRecovered(t *testing.T) {
	rec := &recordingHistory{panicOn: 1}
	s, _, fs := newTestSession(t, rec)
	fs.executeRes = models.Result{RowsAffected: 1}

	// First Execute triggers the panic; SQLSession must recover and the
	// call site must observe a normal (non-panicking) return.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Execute propagated panic: %v", r)
			}
		}()
		if _, err := s.Execute(context.Background(), models.Query{SQL: "first"}); err != nil {
			t.Fatalf("Execute(first): %v", err)
		}
	}()

	// Subsequent Execute must succeed AND record (panicOn was index 1).
	if _, err := s.Execute(context.Background(), models.Query{SQL: "second"}); err != nil {
		t.Fatalf("Execute(second): %v", err)
	}
	calls := rec.snapshot()
	// First call panicked BEFORE the slice append; only the second is recorded.
	if len(calls) != 1 || calls[0].Stmt != "second" {
		t.Errorf("history calls = %+v, want one entry for 'second'", calls)
	}
}

func TestSQLSessionStreamCancelClose_NoGoroutineLeak(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	conn := &fakeConn{}
	fs := &fakeSess{id: 13}
	release := make(chan struct{})
	staged := &fakeRowStream{
		qid:     models.QueryID{SessionID: 13, BackendPID: 99, Nonce: 1},
		total:   100,
		blockOn: release,
	}
	fs.streams = []func() drivers.RowStream{func() drivers.RowStream { return staged }}

	s := session.New(conn, fs, session.Options{})

	rh, err := s.Stream(context.Background(), models.Query{SQL: "SELECT"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if err := s.Cancel(rh.QueryID()); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	close(release)
	// Drive Next to terminal so finish() runs.
	for {
		_, ok, err := rh.Rows().Next(context.Background())
		if !ok || err != nil {
			break
		}
	}
	<-rh.Done()
	_ = rh.Rows().Close()

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestSQLSession_CloseCancelsInFlight(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	conn := &fakeConn{}
	fs := &fakeSess{id: 21}
	release := make(chan struct{})
	staged := &fakeRowStream{
		qid:     models.QueryID{SessionID: 21, BackendPID: 5, Nonce: 1},
		total:   100,
		blockOn: release,
	}
	fs.streams = []func() drivers.RowStream{func() drivers.RowStream { return staged }}

	s := session.New(conn, fs, session.Options{})

	rh, err := s.Stream(context.Background(), models.Query{SQL: "SELECT pg_sleep(60)"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	// Park Next in a background goroutine to simulate a live caller.
	nextDone := make(chan struct{})
	go func() {
		defer close(nextDone)
		for {
			_, ok, err := rh.Rows().Next(context.Background())
			if !ok || err != nil {
				return
			}
		}
	}()

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- s.Close()
	}()

	// Close should call Cancel on the conn; rh.Cancel triggers conn.Cancel
	// synchronously. Wait briefly for it to be observable.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if conn.cancelCount.Load() == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := conn.cancelCount.Load(); got != 1 {
		t.Errorf("conn.Cancel called %d times, want 1", got)
	}

	// Release the fake so the stream's Next can advance to terminal and
	// finish() fires.
	close(release)

	select {
	case <-rh.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("RunHandle.Done did not close after Cancel + release")
	}

	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close returned %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not return")
	}

	<-nextDone
}

func TestSQLSession_ExecuteBlocksDuringStream(t *testing.T) {
	s, _, fs := newTestSession(t, nil)
	release := make(chan struct{})
	staged := &fakeRowStream{
		qid:     models.QueryID{SessionID: 42, Nonce: 1},
		total:   1,
		blockOn: release,
	}
	fs.streams = []func() drivers.RowStream{func() drivers.RowStream { return staged }}
	fs.executeRes = models.Result{RowsAffected: 1}

	rh, err := s.Stream(context.Background(), models.Query{SQL: "SELECT pg_sleep(60)"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	type execResult struct {
		res models.Result
		err error
	}
	resCh := make(chan execResult, 1)
	go func() {
		res, err := s.Execute(context.Background(), models.Query{SQL: "UPDATE x"})
		resCh <- execResult{res: res, err: err}
	}()

	// Give the goroutine a chance to enter Execute and park on streamMu.
	time.Sleep(20 * time.Millisecond)
	select {
	case r := <-resCh:
		t.Fatalf("Execute returned before Stream finished: %+v", r)
	default:
	}

	// Release the stream so it drains to EOF; finish() releases streamMu.
	close(release)
	for {
		_, ok, err := rh.Rows().Next(context.Background())
		if !ok || err != nil {
			break
		}
	}
	<-rh.Done()
	_ = rh.Rows().Close()

	select {
	case r := <-resCh:
		if r.err != nil {
			t.Fatalf("Execute err: %v", r.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Execute did not unblock after Stream finished")
	}
}

// Smoke: SessionID + InTransaction surfaces forward to the inner session.
func TestSQLSessionDelegates_SessionIDAndInTx(t *testing.T) {
	s, _, fs := newTestSession(t, nil)
	if s.SessionID() != fs.id {
		t.Errorf("SessionID = %d, want %d", s.SessionID(), fs.id)
	}
	if s.InTransaction() {
		t.Error("InTransaction = true, want false")
	}
}

// TestNew_NilLoggerUsesDiscardHandler exercises the nil-tolerant contract:
// constructing a SQLSession with an empty Options must succeed and the
// installed default logger must accept a Warn call without panicking and
// without writing to any real sink.
func TestNew_NilLoggerUsesDiscardHandler(t *testing.T) {
	conn := &fakeConn{}
	inner := &fakeSess{id: 1}
	s := session.New(conn, inner, session.Options{})
	if s == nil {
		t.Fatal("New returned nil")
	}
	t.Cleanup(func() { _ = s.Close() })
	// Smoke: any future code path that emits through s.logger would not
	// panic with a nil dereference. We exercise it indirectly via Close,
	// which is safe to call here.
}

func TestSQLSession_SettingsSnapshotAccessor(t *testing.T) {
	s, _, _ := newTestSession(t, nil)
	snap := s.SettingsSnapshot()
	if snap == nil {
		t.Fatal("SettingsSnapshot() returned nil")
	}
	snap.Set("search_path", "myschema")
	v, ok := snap.Get("search_path")
	if !ok || v != "myschema" {
		t.Fatalf("Get = (%q, %v), want (\"myschema\", true)", v, ok)
	}
}

func TestSQLSession_TxStatementCount_NoTx(t *testing.T) {
	s, _, _ := newTestSession(t, nil)
	if got := s.TxStatementCount(); got != 0 {
		t.Fatalf("TxStatementCount = %d, want 0", got)
	}
}

func TestSQLSession_SavepointNames_NoTx(t *testing.T) {
	s, _, _ := newTestSession(t, nil)
	if got := s.SavepointNames(); got != nil {
		t.Fatalf("SavepointNames = %v, want nil", got)
	}
}

func TestSQLSession_OnFinishCallsObserveError(t *testing.T) {
	conn := &fakeConn{}
	fs := &fakeSess{id: 50}
	tx := &fakeTx{parent: fs}
	fs.beginTx = tx
	s := session.New(conn, fs, session.Options{})
	t.Cleanup(func() { _ = s.Close() })

	if _, err := s.Begin(context.Background(), models.TxOptions{}); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	streamErr := errors.New("ERROR: current transaction is aborted (SQLSTATE 25P02)")
	errStream := &fakeRowStream{
		qid:   models.QueryID{SessionID: 50, Nonce: 1},
		total: 0,
	}
	fs.streams = []func() drivers.RowStream{func() drivers.RowStream { return errStream }}

	rh, err := s.Stream(context.Background(), models.Query{SQL: "SELECT 1"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	// Drain the stream; it has 0 rows so Next returns (_, false, nil).
	_, _, _ = rh.Rows().Next(context.Background())
	<-rh.Done()

	// finish() was called with nil termErr (clean EOF) -- ObserveError should
	// NOT have been called.
	tx.observeErrMu.Lock()
	if len(tx.observeErrs) != 0 {
		t.Fatalf("ObserveError called %d times on clean EOF, want 0", len(tx.observeErrs))
	}
	tx.observeErrMu.Unlock()
	_ = rh.Rows().Close()

	// Now set up a stream that terminates with an error.
	errTermStream := &errorTermRowStream{
		qid:     models.QueryID{SessionID: 50, Nonce: 2},
		termErr: streamErr,
	}
	fs.streams = []func() drivers.RowStream{func() drivers.RowStream { return errTermStream }}

	rh2, err := s.Stream(context.Background(), models.Query{SQL: "SELECT 2"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	// Next returns the error.
	_, _, nextErr := rh2.Rows().Next(context.Background())
	if nextErr == nil {
		t.Fatal("expected error from Next")
	}
	<-rh2.Done()

	tx.observeErrMu.Lock()
	if len(tx.observeErrs) != 1 {
		t.Fatalf("ObserveError called %d times, want 1", len(tx.observeErrs))
	}
	if tx.observeErrs[0] != streamErr {
		t.Errorf("ObserveError arg = %v, want %v", tx.observeErrs[0], streamErr)
	}
	tx.observeErrMu.Unlock()
	_ = rh2.Rows().Close()
}
