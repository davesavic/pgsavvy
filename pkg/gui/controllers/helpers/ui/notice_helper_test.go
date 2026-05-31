package ui_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/i18n"
)

// recordingToaster captures every ShowOrUpdate call. Each call is
// recorded as a (key, msg, ttl) tuple; tests read snapshot() to assert
// the sequence.
type recordingToaster struct {
	mu    sync.Mutex
	Calls []toastCall
}

type toastCall struct {
	Key string
	Msg string
	TTL time.Duration
}

func (r *recordingToaster) ShowOrUpdate(key, msg string, ttl time.Duration) {
	r.mu.Lock()
	r.Calls = append(r.Calls, toastCall{Key: key, Msg: msg, TTL: ttl})
	r.mu.Unlock()
}

func (r *recordingToaster) snapshot() []toastCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]toastCall, len(r.Calls))
	copy(out, r.Calls)
	return out
}

func newHelper(t *testing.T, toaster *recordingToaster) *ui.NoticeHelper {
	t.Helper()
	return ui.NewNoticeHelper(ui.NoticeHelperDeps{
		Toaster: toaster,
		Tr:      i18n.EnglishTranslationSet(),
	})
}

func TestNoticeHelper_FirstNoticeRaisesToast_SubsequentUpdatesCounter(t *testing.T) {
	toaster := &recordingToaster{}
	h := newHelper(t, toaster)

	h.OnRunStart("r1")
	for range 3 {
		h.OnNotice(pgconn.Notice{Severity: "NOTICE", Message: "hello"})
	}

	tcalls := toaster.snapshot()
	if len(tcalls) != 3 {
		t.Fatalf("toast calls = %d; want 3", len(tcalls))
	}
	for i, c := range tcalls {
		if c.Key != "r1" {
			t.Errorf("call[%d].Key = %q; want r1", i, c.Key)
		}
	}
	if !strings.Contains(tcalls[2].Msg, "3") {
		t.Fatalf("third call msg = %q; want it to contain count 3", tcalls[2].Msg)
	}
}

func TestNoticeHelper_NewRunAfterOnRunEndRaisesFreshToast(t *testing.T) {
	toaster := &recordingToaster{}
	h := newHelper(t, toaster)

	h.OnRunStart("r1")
	h.OnNotice(pgconn.Notice{Severity: "NOTICE", Message: "first"})
	h.OnRunEnd("r1")

	h.OnRunStart("r2")
	h.OnNotice(pgconn.Notice{Severity: "NOTICE", Message: "second"})

	calls := toaster.snapshot()
	if len(calls) != 2 {
		t.Fatalf("toast calls = %d; want 2", len(calls))
	}
	if calls[0].Key != "r1" {
		t.Errorf("call[0].Key = %q; want r1", calls[0].Key)
	}
	if calls[1].Key != "r2" {
		t.Errorf("call[1].Key = %q; want r2 (fresh run)", calls[1].Key)
	}
	// Subsequent run's count restarts from 1.
	if !strings.Contains(calls[1].Msg, "1") {
		t.Errorf("call[1].Msg = %q; want a fresh count of 1", calls[1].Msg)
	}
}

func TestNoticeHelper_InfoDoesNotToast(t *testing.T) {
	toaster := &recordingToaster{}
	h := newHelper(t, toaster)

	h.OnRunStart("r1")
	h.OnNotice(pgconn.Notice{Severity: "INFO", Message: "fyi"})

	if got := len(toaster.snapshot()); got != 0 {
		t.Fatalf("INFO produced %d toast calls; want 0", got)
	}
}

func TestNoticeHelper_UnknownRunIDDropped(t *testing.T) {
	toaster := &recordingToaster{}
	h := newHelper(t, toaster)

	// No OnRunStart — notice arrives when no run is bound.
	h.OnNotice(pgconn.Notice{Severity: "NOTICE", Message: "orphan"})

	if got := len(toaster.snapshot()); got != 0 {
		t.Fatalf("toast calls = %d; want 0 (notice dropped)", got)
	}
}

func TestNoticeHelper_NoOpWhenNoNotices(t *testing.T) {
	toaster := &recordingToaster{}
	h := newHelper(t, toaster)

	h.OnRunStart("r1")
	h.Finish("r1")
	h.OnRunEnd("r1")

	if got := len(toaster.snapshot()); got != 0 {
		t.Fatalf("toast calls = %d; want 0", got)
	}
}

func TestNoticeHelper_ConcurrentOnNotice100x(t *testing.T) {
	toaster := &recordingToaster{}
	h := newHelper(t, toaster)

	h.OnRunStart("r1")

	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			h.OnNotice(pgconn.Notice{Severity: "NOTICE", Message: "concurrent"})
		})
	}
	wg.Wait()

	calls := toaster.snapshot()
	if got := len(calls); got != 100 {
		t.Fatalf("toast calls = %d; want 100", got)
	}
	// The final-count assertion: at least one of the 100 calls must
	// have reported the terminal count. Ordering between worker
	// goroutines isn't guaranteed, so we scan for a call with "100".
	foundFinal := false
	for _, c := range calls {
		if strings.Contains(c.Msg, "100") {
			foundFinal = true
			break
		}
	}
	if !foundFinal {
		t.Fatalf("no toast call carried the final count 100; calls=%+v", calls)
	}
}
