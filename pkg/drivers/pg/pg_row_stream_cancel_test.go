package pg

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/davesavic/dbsavvy/pkg/models"
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
// statement-timeout lifecycle (dbsavvy-fow.7 U15): a leaked CancelFunc
// would strand the deadline timer goroutine past release.
func TestPgRowStreamReleaseInvokesCancelOnce(t *testing.T) {
	var cancelCalls atomic.Int32
	cancel := func() { cancelCalls.Add(1) }

	rows := &fakeStreamRows{}
	stream := newPgRowStream(rows, models.QueryID{}, func() {}, cancel)

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
	stream := newPgRowStream(rows, models.QueryID{}, func() {}, cancel)

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
	stream := newPgRowStream(rows, models.QueryID{}, func() {}, nil)
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
	stream := newPgRowStream(rows, models.QueryID{}, func() {}, cancel)

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
