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
