package ui

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/session"
)

// defaultNoticeToastTTL is the lifetime applied to NOTICE/WARNING
// toasts when NoticeHelperDeps.ToastTTL is zero. 4s matches the rest of
// the query-editor toast surface (queryToastTTL in controllers).
const defaultNoticeToastTTL = 4 * time.Second

// noticeToaster is the narrow surface NoticeHelper uses to emit a
// keyed toast. *ToastHelper satisfies it (ShowOrUpdate is defined on
// the concrete type). Kept unexported because the only legitimate
// production implementation is ToastHelper; tests inject a fake from
// the same package _test scope via the exported NoticeHelperDeps.
type noticeToaster interface {
	ShowOrUpdate(key, message string, ttl time.Duration)
}

// NoticeHelperDeps bundles the collaborators required to construct a
// NoticeHelper. Toaster may be nil (toasting disabled); OnWorker may be
// nil (callers feed OnNotice directly — test-friendly); Tr is required
// because the toast format strings live there.
type NoticeHelperDeps struct {
	Toaster  noticeToaster
	OnWorker func(func(gocui.Task) error)
	Tr       *i18n.TranslationSet
	ToastTTL time.Duration
}

// NoticeReporter is the controller-facing surface for routing server
// NOTICE / WARNING messages from streaming queries. The controller
// calls OnRunStart before launching a run, AttachStream for each
// RunHandle the run produces, and Finish once no more streams will
// attach; OnRunEnd then fires automatically when the last attached
// stream's notice channel drains.
type NoticeReporter interface {
	OnRunStart(runID string)
	OnRunEnd(runID string)
	OnNotice(n pgconn.Notice)
	AttachStream(rh *session.RunHandle)
	Finish(runID string)
}

// NoticeHelper routes server NOTICE/WARNING messages from a streaming
// query into a first-of-run toast (NOTICE/WARNING only; counter-updates
// on subsequent emissions in the same run via ToastHelper.ShowOrUpdate).
// The helper is run-scoped — OnRunStart establishes a runID,
// AttachStream spawns a drain worker per RunHandle, and Finish signals
// "no more streams"; once every attached stream's notice channel
// closes the helper resets its run state.
type NoticeHelper struct {
	toaster  noticeToaster
	onWorker func(func(gocui.Task) error)
	tr       *i18n.TranslationSet
	toastTTL time.Duration

	mu             sync.Mutex
	currentRun     string
	noticeCount    int
	pendingStreams int
	finishing      bool
}

// NewNoticeHelper constructs a NoticeHelper from deps. A nil deps.Tr
// is replaced with a fresh English baseline so tests can omit it.
func NewNoticeHelper(deps NoticeHelperDeps) *NoticeHelper {
	tr := deps.Tr
	if tr == nil {
		tr = i18n.EnglishTranslationSet()
	}
	ttl := deps.ToastTTL
	if ttl <= 0 {
		ttl = defaultNoticeToastTTL
	}
	return &NoticeHelper{
		toaster:  deps.Toaster,
		onWorker: deps.OnWorker,
		tr:       tr,
		toastTTL: ttl,
	}
}

// OnRunStart resets the helper's run state and binds it to runID. The
// next OnNotice call counted against runID will be the first-of-run
// toast. Calling OnRunStart while a run is in flight discards the
// prior run's counters — the controller is responsible for sequencing.
func (h *NoticeHelper) OnRunStart(runID string) {
	h.mu.Lock()
	h.currentRun = runID
	h.noticeCount = 0
	h.pendingStreams = 0
	h.finishing = false
	h.mu.Unlock()
}

// OnRunEnd clears the helper's run state if (and only if) runID matches
// the currently bound run. Mismatched runIDs are a no-op — a stale
// stream worker that finishes after a new run started must not stomp
// the new run's counters.
func (h *NoticeHelper) OnRunEnd(runID string) {
	h.mu.Lock()
	if h.currentRun == runID {
		h.currentRun = ""
		h.noticeCount = 0
		h.pendingStreams = 0
		h.finishing = false
	}
	h.mu.Unlock()
}

// OnNotice processes a single pgconn.Notice. When severity is NOTICE or
// WARNING it raises a first-of-run toast or counter-updates the existing
// one. Notices delivered while no run is bound (e.g. a stale drain-
// worker firing after OnRunEnd) are dropped entirely.
func (h *NoticeHelper) OnNotice(n pgconn.Notice) {
	h.mu.Lock()
	if h.currentRun == "" {
		h.mu.Unlock()
		return
	}
	runID := h.currentRun
	toastable := isToastableSeverity(n.Severity)
	var count int
	var isFirst bool
	if toastable {
		h.noticeCount++
		count = h.noticeCount
		isFirst = count == 1
	}
	h.mu.Unlock()

	if !toastable || h.toaster == nil {
		return
	}
	var format string
	if isFirst {
		format = h.tr.NoticeToastFirst
	} else {
		format = h.tr.NoticeToastSubsequent
	}
	h.toaster.ShowOrUpdate(runID, fmt.Sprintf(format, count), h.toastTTL)
}

// AttachStream spawns a drain worker that copies rh.Notices() into
// OnNotice and signals the helper when the stream's notice channel
// closes. When OnWorker is nil the call records a pending-stream slot
// but does not spawn a worker — tests drive OnNotice directly.
func (h *NoticeHelper) AttachStream(rh *session.RunHandle) {
	if rh == nil {
		return
	}
	h.mu.Lock()
	if h.currentRun == "" {
		h.mu.Unlock()
		return
	}
	runID := h.currentRun
	h.pendingStreams++
	h.mu.Unlock()

	if h.onWorker == nil {
		// Test path: caller drives OnNotice directly; the pending slot
		// remains until Finish triggers the inline drain below.
		return
	}
	h.onWorker(func(_ gocui.Task) error {
		for n := range rh.Notices() {
			h.OnNotice(n)
		}
		h.streamFinished(runID)
		return nil
	})
}

// Finish marks runID as receiving no further AttachStream calls. If
// every attached stream has already drained, the run state clears
// inline; otherwise the last drain-worker to finish clears it.
func (h *NoticeHelper) Finish(runID string) {
	h.mu.Lock()
	if h.currentRun != runID {
		h.mu.Unlock()
		return
	}
	h.finishing = true
	clear := h.pendingStreams == 0
	if clear {
		h.currentRun = ""
		h.noticeCount = 0
		h.pendingStreams = 0
		h.finishing = false
	}
	h.mu.Unlock()
}

// streamFinished decrements the pending-stream count and clears the
// run state when finishing and every stream has drained.
func (h *NoticeHelper) streamFinished(runID string) {
	h.mu.Lock()
	if h.currentRun != runID {
		h.mu.Unlock()
		return
	}
	if h.pendingStreams > 0 {
		h.pendingStreams--
	}
	if h.finishing && h.pendingStreams == 0 {
		h.currentRun = ""
		h.noticeCount = 0
		h.pendingStreams = 0
		h.finishing = false
	}
	h.mu.Unlock()
}

// isToastableSeverity returns true for severities that surface as a
// toast. NOTICE and WARNING toast; INFO and everything else are ignored.
func isToastableSeverity(raw string) bool {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "NOTICE", "WARNING":
		return true
	default:
		return false
	}
}
