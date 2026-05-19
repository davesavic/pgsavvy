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

// recordingSink is a synchronous MessagesSink fake; Append captures
// each line into Lines under the mutex.
type recordingSink struct {
	mu    sync.Mutex
	Lines []string
}

func (r *recordingSink) Append(line string) {
	r.mu.Lock()
	r.Lines = append(r.Lines, line)
	r.mu.Unlock()
}

func (r *recordingSink) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.Lines))
	copy(out, r.Lines)
	return out
}

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

func newHelper(t *testing.T, sink ui.MessagesSink, toaster *recordingToaster) *ui.NoticeHelper {
	t.Helper()
	return ui.NewNoticeHelper(ui.NoticeHelperDeps{
		Sink:    sink,
		Toaster: toaster,
		Tr:      i18n.EnglishTranslationSet(),
	})
}

func TestNoticeHelper_FirstNoticeRaisesToast_SubsequentUpdatesCounter(t *testing.T) {
	sink := &recordingSink{}
	toaster := &recordingToaster{}
	h := newHelper(t, sink, toaster)

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
	lines := sink.snapshot()
	if len(lines) != 3 {
		t.Fatalf("sink lines = %d; want 3", len(lines))
	}
}

func TestNoticeHelper_NewRunAfterOnRunEndRaisesFreshToast(t *testing.T) {
	sink := &recordingSink{}
	toaster := &recordingToaster{}
	h := newHelper(t, sink, toaster)

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

func TestNoticeHelper_InfoDoesNotToastButIsLogged(t *testing.T) {
	sink := &recordingSink{}
	toaster := &recordingToaster{}
	h := newHelper(t, sink, toaster)

	h.OnRunStart("r1")
	h.OnNotice(pgconn.Notice{Severity: "INFO", Message: "fyi"})

	if got := len(toaster.snapshot()); got != 0 {
		t.Fatalf("INFO produced %d toast calls; want 0", got)
	}
	lines := sink.snapshot()
	if len(lines) != 1 {
		t.Fatalf("sink lines = %d; want 1", len(lines))
	}
	if !strings.HasPrefix(lines[0], "[INFO]") {
		t.Errorf("line = %q; want [INFO] prefix", lines[0])
	}
}

func TestNoticeHelper_UnknownRunIDDropped(t *testing.T) {
	sink := &recordingSink{}
	toaster := &recordingToaster{}
	h := newHelper(t, sink, toaster)

	// No OnRunStart — notice arrives when no run is bound.
	h.OnNotice(pgconn.Notice{Severity: "NOTICE", Message: "orphan"})

	if got := len(toaster.snapshot()); got != 0 {
		t.Fatalf("toast calls = %d; want 0 (notice dropped)", got)
	}
	if got := len(sink.snapshot()); got != 0 {
		t.Fatalf("sink lines = %d; want 0 (notice dropped entirely)", got)
	}
}

func TestNoticeHelper_NoOpWhenNoNotices(t *testing.T) {
	sink := &recordingSink{}
	toaster := &recordingToaster{}
	h := newHelper(t, sink, toaster)

	h.OnRunStart("r1")
	h.Finish("r1")
	h.OnRunEnd("r1")

	if got := len(toaster.snapshot()); got != 0 {
		t.Fatalf("toast calls = %d; want 0", got)
	}
	if got := len(sink.snapshot()); got != 0 {
		t.Fatalf("sink lines = %d; want 0", got)
	}
}

func TestNoticeHelper_SeverityPrefixInMessages(t *testing.T) {
	sink := &recordingSink{}
	toaster := &recordingToaster{}
	h := newHelper(t, sink, toaster)

	h.OnRunStart("r1")
	h.OnNotice(pgconn.Notice{Severity: "NOTICE", Message: "n-msg"})
	h.OnNotice(pgconn.Notice{Severity: "WARNING", Message: "w-msg"})

	lines := sink.snapshot()
	if len(lines) != 2 {
		t.Fatalf("sink lines = %d; want 2", len(lines))
	}
	if !strings.HasPrefix(lines[0], "[NOTICE]·") {
		t.Errorf("line[0] = %q; want [NOTICE]· prefix", lines[0])
	}
	if !strings.HasPrefix(lines[1], "[WARNING]·") {
		t.Errorf("line[1] = %q; want [WARNING]· prefix", lines[1])
	}
}

func TestNoticeHelper_ConcurrentOnNotice100x(t *testing.T) {
	sink := &recordingSink{}
	toaster := &recordingToaster{}
	h := newHelper(t, sink, toaster)

	h.OnRunStart("r1")

	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			h.OnNotice(pgconn.Notice{Severity: "NOTICE", Message: "concurrent"})
		})
	}
	wg.Wait()

	if got := len(sink.snapshot()); got != 100 {
		t.Fatalf("sink lines = %d; want 100", got)
	}
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
