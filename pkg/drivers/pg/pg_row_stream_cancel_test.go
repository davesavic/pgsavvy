package pg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// fakeStreamRows is a minimal pgx.Rows double for the pgRowStream
// lifecycle unit tests. It replays no rows by default (Next returns false
// immediately so the first Next hits the EOF-release path).
type fakeStreamRows struct {
	remaining int
	err       error
	closed    int32
}

func (r *fakeStreamRows) Close()                        { atomic.AddInt32(&r.closed, 1) }
func (r *fakeStreamRows) Err() error                    { return r.err }
func (r *fakeStreamRows) CommandTag() pgconn.CommandTag { return pgconn.CommandTag{} }
func (r *fakeStreamRows) FieldDescriptions() []pgconn.FieldDescription {
	return nil
}

func (r *fakeStreamRows) Next() bool {
	if r.remaining <= 0 {
		return false
	}
	r.remaining--
	return true
}

func (r *fakeStreamRows) Scan(_ ...any) error { return nil }
func (r *fakeStreamRows) Values() ([]any, error) {
	return []any{1}, nil
}
func (r *fakeStreamRows) RawValues() [][]byte { return nil }
func (r *fakeStreamRows) Conn() *pgx.Conn     { return nil }

var _ pgx.Rows = (*fakeStreamRows)(nil)

// TestPgRowStreamReleaseInvokesCancelOnce asserts the timeout-derived
// context.CancelFunc captured at construction is invoked EXACTLY ONCE when
// the stream releases — and that repeated Close / EOF-release calls do not
// fire it again. This is the load-bearing once/CAS guard for the
// statement-timeout lifecycle: a leaked CancelFunc
// would strand the deadline timer goroutine past release.
func TestPgRowStreamReleaseInvokesCancelOnce(t *testing.T) {
	var cancelCalls atomic.Int32
	cancel := func() { cancelCalls.Add(1) }

	rows := &fakeStreamRows{}
	stream := newPgRowStream(rows, models.QueryID{}, func() {}, cancel, nil)

	// First Close runs the cleanup → cancel fires once.
	if err := stream.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Second Close is a no-op (CAS guard) → cancel must NOT fire again.
	if err := stream.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	if got := cancelCalls.Load(); got != 1 {
		t.Fatalf("cancel invoked %d times, want exactly 1", got)
	}
	if got := atomic.LoadInt32(&rows.closed); got != 1 {
		t.Fatalf("rows.Close invoked %d times, want exactly 1", got)
	}
}

// TestPgRowStreamReleaseCancelViaEOFThenClose asserts the EOF-release path
// (Next observing clean end-of-result) fires the cancel exactly once, and a
// follow-up explicit Close does not fire it again.
func TestPgRowStreamReleaseCancelViaEOFThenClose(t *testing.T) {
	var cancelCalls atomic.Int32
	cancel := func() { cancelCalls.Add(1) }

	rows := &fakeStreamRows{remaining: 0} // immediate EOF
	stream := newPgRowStream(rows, models.QueryID{}, func() {}, cancel, nil)

	if _, ok, err := stream.Next(context.Background()); ok || err != nil {
		t.Fatalf("Next on empty stream = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close after EOF: %v", err)
	}

	if got := cancelCalls.Load(); got != 1 {
		t.Fatalf("cancel invoked %d times, want exactly 1 (EOF path + idempotent Close)", got)
	}
}

// TestPgRowStreamNilCancelIsSafe confirms a zero-timeout stream (no derived
// deadline → nil CancelFunc) releases cleanly without panicking.
func TestPgRowStreamNilCancelIsSafe(t *testing.T) {
	rows := &fakeStreamRows{}
	stream := newPgRowStream(rows, models.QueryID{}, func() {}, nil, nil)
	if err := stream.Close(); err != nil {
		t.Fatalf("Close with nil cancel: %v", err)
	}
}

// TestPgRowStreamReleaseConcurrentCancelOnce hammers release() from many
// goroutines and asserts the cancel still fires exactly once — the CAS
// guard must be race-safe.
func TestPgRowStreamReleaseConcurrentCancelOnce(t *testing.T) {
	var cancelCalls atomic.Int32
	cancel := func() { cancelCalls.Add(1) }

	rows := &fakeStreamRows{}
	stream := newPgRowStream(rows, models.QueryID{}, func() {}, cancel, nil)

	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			_ = stream.Close()
		})
	}
	wg.Wait()

	if got := cancelCalls.Load(); got != 1 {
		t.Fatalf("cancel invoked %d times under concurrency, want exactly 1", got)
	}
}

func TestPgRowStreamCancelJoinsBeforeCleanup(t *testing.T) {
	queryCtx, cancelQuery := context.WithCancel(context.Background())
	rows := &blockedStreamRows{
		ctx: queryCtx, entered: make(chan struct{}), leave: make(chan struct{}),
		exited: make(chan struct{}), closing: make(chan struct{}),
	}
	var leaveOnce sync.Once
	unblock := func() { leaveOnce.Do(func() { close(rows.leave) }) }
	var eventsMu sync.Mutex
	var events []string
	record := func(event string) {
		eventsMu.Lock()
		defer eventsMu.Unlock()
		events = append(events, event)
	}
	rows.record = record
	guard := &Session{}
	guard.acquireInFlight()
	var cancels, samples, releases atomic.Int32
	stream := newPgRowStream(rows, models.QueryID{SessionID: 1}, func() {
		record("release")
		releases.Add(1)
		guard.releaseInFlight()
	}, func() {
		record("cancel")
		cancels.Add(1)
		cancelQuery()
	}, func() { samples.Add(1) })
	consumer, cancelConsumer := context.WithCancel(context.Background())
	nextDone := make(chan struct{})
	var nextErr error
	t.Cleanup(func() {
		cancelConsumer()
		cancelQuery()
		unblock()
		waitStreamSignal(t, nextDone, "rescued Next")
		_ = stream.Close()
	})
	go func() { _, _, nextErr = stream.Next(consumer); close(nextDone) }()
	waitStreamSignal(t, rows.entered, "reader entry")
	cancelConsumer()
	select {
	case <-queryCtx.Done():
	case <-rows.closing:
		t.Error("cleanup started before the query context was cancelled")
		cancelQuery()
	case <-time.After(2 * time.Second):
		t.Error("consumer did not cancel the original query")
		cancelQuery()
	}
	closed := make(chan struct{})
	go func() { _ = stream.Close(); close(closed) }()
	// Keep the reader alive after cancellation: neither cleanup nor guard
	// release is safe until it acknowledges the interrupt and exits.
	select {
	case <-rows.closing:
		t.Error("cleanup overlapped the active reader")
	case <-nextDone:
		t.Error("Next returned before the active reader exited")
	case <-time.After(25 * time.Millisecond):
	}
	if guard.inFlight.Load() != 1 || releases.Load() != 0 {
		t.Error("session guard released while reader was active")
	}
	unblock()
	waitStreamSignal(t, nextDone, "Next completion")
	waitStreamSignal(t, rows.exited, "reader exit")
	waitStreamSignal(t, closed, "concurrent Close completion")
	if !errors.Is(nextErr, context.Canceled) {
		t.Errorf("Next error=%v, want context.Canceled", nextErr)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if cancels.Load() != 1 || releases.Load() != 1 || atomic.LoadInt32(&rows.closed) != 1 || samples.Load() != 0 {
		t.Errorf("lifecycle counts: cancel=%d release=%d close=%d sample=%d", cancels.Load(), releases.Load(), atomic.LoadInt32(&rows.closed), samples.Load())
	}
	eventsMu.Lock()
	gotEvents := append([]string(nil), events...)
	eventsMu.Unlock()
	if want := []string{"cancel", "reader-exit", "close", "release"}; !reflect.DeepEqual(gotEvents, want) {
		t.Errorf("lifecycle order=%v, want %v", gotEvents, want)
	}
	guard.acquireInFlight()
	guard.releaseInFlight()
}

func TestPgRowStreamOrdinaryCloseDrainsBeforeCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rows := &orderedCloseRows{ctx: ctx}
	var samples atomic.Int32
	var events []string
	stream := newPgRowStream(rows, models.QueryID{}, func() { events = append(events, "release") }, cancel, func() {
		samples.Add(1)
		events = append(events, "sample")
	})
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if rows.cancelledAtClose || ctx.Err() == nil || samples.Load() != 1 {
		t.Fatalf("ordinary Close changed drain policy: cancelledAtClose=%v final=%v samples=%d", rows.cancelledAtClose, ctx.Err(), samples.Load())
	}
	if want := []string{"sample", "release"}; !reflect.DeepEqual(events, want) {
		t.Errorf("sample must precede guard release: %v", events)
	}
}

func TestSessionStreamConsumerCancelOwnsQueryContext(t *testing.T) {
	for _, timeout := range []time.Duration{0, time.Hour} {
		t.Run(timeout.String(), func(t *testing.T) {
			poolConn := openStalledProtocolSession(t)
			sess := &Session{conn: poolConn, id: 1}
			parent, rescue := context.WithCancel(context.Background())
			defer rescue()
			stream, err := sess.Stream(parent, models.Query{SQL: "SELECT stalled", Timeout: timeout})
			if err != nil {
				t.Fatal(err)
			}
			consumer, cancelConsumer := context.WithCancel(context.Background())
			cancelConsumer()
			done := make(chan error, 1)
			go func() { _, _, err := stream.Next(consumer); done <- err }()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("Next error=%v, want context.Canceled", err)
				}
			case <-time.After(time.Second):
				t.Error("consumer cancellation did not interrupt original query before draining")
				rescue()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Fatal("rescue did not release Next")
				}
			}
			if parent.Err() != nil && !t.Failed() {
				t.Error("stream cancelled caller-owned parent context")
			}
			if sess.inFlight.Load() != 0 {
				t.Error("stream retained driver guard")
			}
			if err := stream.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type blockedStreamRows struct {
	fakeStreamRows
	ctx     context.Context
	entered chan struct{}
	leave   chan struct{}
	exited  chan struct{}
	closing chan struct{}
	record  func(string)
}

func (r *blockedStreamRows) Next() bool {
	close(r.entered)
	<-r.ctx.Done()
	<-r.leave
	r.record("reader-exit")
	close(r.exited)
	return false
}

func (r *blockedStreamRows) Close() {
	r.record("close")
	close(r.closing)
	<-r.ctx.Done()
	r.fakeStreamRows.Close()
}

func (r *blockedStreamRows) Err() error { return r.ctx.Err() }

type orderedCloseRows struct {
	fakeStreamRows
	ctx              context.Context
	cancelledAtClose bool
}

func (r *orderedCloseRows) Close() {
	r.cancelledAtClose = r.ctx.Err() != nil
	r.fakeStreamRows.Close()
}

func waitStreamSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// Use the real pgx query/context machinery over an in-memory wire, without a
// PostgreSQL fixture. The peer supplies metadata but never completes the row.
func openStalledProtocolSession(t *testing.T) *pgxpool.Conn {
	t.Helper()
	client, peer := net.Pipe()
	serverDone := make(chan error, 1)
	go func() {
		backend := pgproto3.NewBackend(peer, peer)
		if _, err := backend.ReceiveStartupMessage(); err != nil {
			serverDone <- err
			return
		}
		backend.Send(&pgproto3.AuthenticationOk{})
		backend.Send(&pgproto3.ParameterStatus{Name: "standard_conforming_strings", Value: "on"})
		backend.Send(&pgproto3.ParameterStatus{Name: "client_encoding", Value: "UTF8"})
		backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
		if err := backend.Flush(); err != nil {
			serverDone <- err
			return
		}
		msg, err := backend.Receive()
		if err != nil {
			serverDone <- err
			return
		}
		if _, ok := msg.(*pgproto3.Query); !ok {
			serverDone <- fmt.Errorf("unexpected query message %T", msg)
			return
		}
		backend.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("n"), DataTypeOID: 23, DataTypeSize: 4, TypeModifier: -1}}})
		if err := backend.Flush(); err != nil {
			serverDone <- err
			return
		}
		// Drain the client's termination write so pgx can close its pipe.
		_, err = io.Copy(io.Discard, peer)
		serverDone <- err
	}()
	t.Cleanup(func() {
		_ = client.Close()
		_ = peer.Close()
		select {
		case err := <-serverDone:
			if err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.ErrClosedPipe) {
				t.Errorf("synthetic server: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("synthetic server did not exit")
		}
	})
	cfg, err := pgxpool.ParseConfig("postgres://test@127.0.0.1/test?sslmode=disable&default_query_exec_mode=simple_protocol")
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	var dialed atomic.Bool
	cfg.ConnConfig.DialFunc = func(context.Context, string, string) (net.Conn, error) {
		if !dialed.CompareAndSwap(false, true) {
			return nil, errors.New("synthetic peer supports only the original connection")
		}
		return client, nil
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = peer.Close()
		pool.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.Release)
	return conn
}
