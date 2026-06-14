package session

import (
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestRunHandle_RouteNoticeAfterFinish_NoPanic guards the send-on-closed-channel
// race: the session fan-out goroutine may call routeNotice after finish() has
// already closed the notice channel (it loaded runActive before the run
// terminated). The late notice must be dropped, not sent on the closed channel.
func TestRunHandle_RouteNoticeAfterFinish_NoPanic(t *testing.T) {
	rh := &RunHandle{
		done:    make(chan struct{}),
		notices: make(chan pgconn.Notice, runNoticeBuffer),
	}

	rh.finish(nil) // closes rh.notices, as a terminal Next()/Close() would

	// fanOutNotices loaded this rh before the close and now routes a late
	// notice — this must not panic.
	rh.routeNotice(pgconn.Notice{Message: "late"})

	if got := rh.DroppedNotices(); got != 1 {
		t.Errorf("DroppedNotices() = %d, want 1 (late notice dropped, not sent)", got)
	}
}

// TestRunHandle_Err_RecordsTerminalError verifies Err() reports the terminal
// error observed at finish() once Done has closed, and nil for a clean EOF.
// Post-run consumers success-gate metadata invalidation on
// this.
func TestRunHandle_Err_RecordsTerminalError(t *testing.T) {
	t.Run("clean EOF -> nil", func(t *testing.T) {
		rh := &RunHandle{done: make(chan struct{}), notices: make(chan pgconn.Notice, runNoticeBuffer)}
		rh.finish(nil)
		<-rh.Done()
		if rh.Err() != nil {
			t.Errorf("Err() = %v, want nil after clean finish", rh.Err())
		}
	})
	t.Run("terminal error recorded", func(t *testing.T) {
		want := errReturned
		rh := &RunHandle{done: make(chan struct{}), notices: make(chan pgconn.Notice, runNoticeBuffer)}
		rh.finish(want)
		<-rh.Done()
		if rh.Err() != want {
			t.Errorf("Err() = %v, want %v", rh.Err(), want)
		}
	})
	t.Run("first finish wins", func(t *testing.T) {
		rh := &RunHandle{done: make(chan struct{}), notices: make(chan pgconn.Notice, runNoticeBuffer)}
		rh.finish(nil)
		rh.finish(errReturned) // second finish is a no-op (sync.Once)
		if rh.Err() != nil {
			t.Errorf("Err() = %v, want nil (first finish wins)", rh.Err())
		}
	})
}

var errReturned = errSentinel("boom")

type errSentinel string

func (e errSentinel) Error() string { return string(e) }
