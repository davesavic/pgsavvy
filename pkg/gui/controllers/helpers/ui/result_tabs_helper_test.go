package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// fakeRunHandle is a runHandle test double. doneCh is closed by the
// test to simulate stream termination; Cancel records the call and
// closes doneCh (idempotent).
type fakeRunHandle struct {
	doneCh    chan struct{}
	cancelMu  sync.Mutex
	cancelled bool
	cancelErr error
	rows      drivers.RowStream
}

func newFakeRunHandle() *fakeRunHandle {
	return &fakeRunHandle{doneCh: make(chan struct{})}
}

func (f *fakeRunHandle) Done() <-chan struct{} { return f.doneCh }
func (f *fakeRunHandle) Cancel() error {
	f.cancelMu.Lock()
	defer f.cancelMu.Unlock()
	if f.cancelled {
		return nil
	}
	f.cancelled = true
	select {
	case <-f.doneCh:
	default:
		close(f.doneCh)
	}
	return f.cancelErr
}
func (f *fakeRunHandle) Rows() drivers.RowStream { return f.rows }

func (f *fakeRunHandle) wasCancelled() bool {
	f.cancelMu.Lock()
	defer f.cancelMu.Unlock()
	return f.cancelled
}

func (f *fakeRunHandle) finish() {
	f.cancelMu.Lock()
	defer f.cancelMu.Unlock()
	select {
	case <-f.doneCh:
	default:
		close(f.doneCh)
	}
}

// fakeToaster records every Show call.
type fakeToaster struct {
	mu       sync.Mutex
	messages []string
}

func (f *fakeToaster) Show(msg string, ttl time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = append(f.messages, msg)
}

func (f *fakeToaster) Messages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.messages))
	copy(out, f.messages)
	return out
}

func (f *fakeToaster) Last() string {
	msgs := f.Messages()
	if len(msgs) == 0 {
		return ""
	}
	return msgs[len(msgs)-1]
}

// fakeStreamRunner records NewQueryTask invocations.
type fakeStreamRunner struct {
	mu             sync.Mutex
	starts         int
	stops          int
	lastKey        string
	lastInit       int
	readRowsCalls  []int
	readToEndCalls int
	lastOnDone     func()
	estimatedRows  int64
}

func (f *fakeStreamRunner) NewQueryTask(
	taskKey string,
	_ func(ctx context.Context) (drivers.RowStream, error),
	_ func([]models.Row),
	initialRows int,
	onDone func(),
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	f.lastKey = taskKey
	f.lastInit = initialRows
	f.lastOnDone = onDone
	return nil
}

func (f *fakeStreamRunner) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
}

func (f *fakeStreamRunner) ReadRows(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readRowsCalls = append(f.readRowsCalls, n)
}

func (f *fakeStreamRunner) ReadToEnd(then func()) {
	f.mu.Lock()
	f.readToEndCalls++
	f.mu.Unlock()
	if then != nil {
		then()
	}
}

func (f *fakeStreamRunner) EstimatedRows() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.estimatedRows
}

func (f *fakeStreamRunner) setEstimatedRows(n int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.estimatedRows = n
}

func (f *fakeStreamRunner) StartCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts
}

func (f *fakeStreamRunner) StopCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stops
}

func (f *fakeStreamRunner) ReadRowsCalls() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int, len(f.readRowsCalls))
	copy(out, f.readRowsCalls)
	return out
}

func (f *fakeStreamRunner) ReadToEndCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.readToEndCalls
}

func (f *fakeStreamRunner) fireOnDone() {
	f.mu.Lock()
	cb := f.lastOnDone
	f.mu.Unlock()
	if cb != nil {
		cb()
	}
}

// newTestHelper builds a helper with the common test deps wired.
func newTestHelper(t *testing.T, factory StreamRunnerFactory) (*ResultTabsHelper, *fakeToaster) {
	t.Helper()
	toaster := &fakeToaster{}
	deps := ResultTabsHelperDeps{
		Toast:         toaster,
		MaxTabs:       0, // -> DefaultMaxResultTabs (8)
		StreamFactory: factory,
		Now:           time.Now,
	}
	return NewResultTabsHelper(deps), toaster
}

// --- Open / Active / Jump --------------------------------------------------

func TestOpenThreeTabsAndJump(t *testing.T) {
	h, toaster := newTestHelper(t, nil)

	for i, sql := range []string{"q1", "q2", "q3"} {
		if err := h.openTab(sql, nil); err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
	}

	tabs := h.Tabs()
	if len(tabs) != 3 {
		t.Fatalf("Tabs len = %d, want 3", len(tabs))
	}
	for i, want := range []string{"q1", "q2", "q3"} {
		if tabs[i].Label() != want {
			t.Errorf("tab[%d].Label() = %q, want %q", i, tabs[i].Label(), want)
		}
		if tabs[i].Slot() != i {
			t.Errorf("tab[%d].Slot() = %d, want %d", i, tabs[i].Slot(), i)
		}
	}

	// Active after sequential Open should be the most recent.
	if active := h.Active(); active == nil || active.Label() != "q3" {
		t.Fatalf("Active() = %v, want q3", active)
	}

	// <leader>1 -> slot 0 -> q1
	h.Jump(1)
	if active := h.Active(); active == nil || active.Label() != "q1" {
		t.Fatalf("after Jump(1) Active() = %v, want q1", active)
	}
	_ = toaster
}

func TestJumpOutOfRangeToasts(t *testing.T) {
	h, toaster := newTestHelper(t, nil)
	_ = h.openTab("q1", nil)
	h.Jump(5)
	if got := toaster.Last(); got != "no tab 5" {
		t.Errorf("toast = %q, want %q", got, "no tab 5")
	}
}

func TestJumpWithNoTabsToasts(t *testing.T) {
	h, toaster := newTestHelper(t, nil)
	h.Jump(1)
	if got := toaster.Last(); got != "no result tabs" {
		t.Errorf("toast = %q, want 'no result tabs'", got)
	}
}

// --- Cycle ----------------------------------------------------------------

func TestCycleWrapsAroundBoundaries(t *testing.T) {
	h, _ := newTestHelper(t, nil)
	for _, sql := range []string{"a", "b", "c"} {
		_ = h.openTab(sql, nil)
	}
	// Active is c (slot 2).
	h.Cycle(1) // wraps to slot 0
	if a := h.Active(); a == nil || a.Label() != "a" {
		t.Fatalf("after Cycle(+1) from c: Active = %v, want a", a)
	}
	h.Cycle(-1) // wraps back to slot 2
	if a := h.Active(); a == nil || a.Label() != "c" {
		t.Fatalf("after Cycle(-1) from a: Active = %v, want c", a)
	}
}

// --- Close ----------------------------------------------------------------

func TestCloseActiveShiftsToPrevSlot(t *testing.T) {
	h, _ := newTestHelper(t, nil)
	for _, sql := range []string{"a", "b", "c"} {
		_ = h.openTab(sql, nil)
	}
	// Active is c at slot 2. Close it -> active becomes slot 1 (b).
	if err := h.CloseActive(); err != nil {
		t.Fatalf("CloseActive: %v", err)
	}
	if a := h.Active(); a == nil || a.Label() != "b" {
		t.Fatalf("after CloseActive: Active = %v, want b", a)
	}
	// Close b -> active becomes slot 0 (a).
	_ = h.CloseActive()
	if a := h.Active(); a == nil || a.Label() != "a" {
		t.Fatalf("after second CloseActive: Active = %v, want a", a)
	}
	// Close a -> no tabs left.
	_ = h.CloseActive()
	if a := h.Active(); a != nil {
		t.Fatalf("after closing last tab: Active = %v, want nil", a)
	}
}

func TestCloseActiveOnEmptyToasts(t *testing.T) {
	h, toaster := newTestHelper(t, nil)
	_ = h.CloseActive()
	if got := toaster.Last(); got != "no result tabs" {
		t.Errorf("toast = %q, want 'no result tabs'", got)
	}
}

// TestCloseActiveFiresOnActiveClosed: a user-initiated close of the
// focused tab must fire onActiveClosed so the orchestrator can reconcile
// the focus stack (dbsavvy-aqw). The closed tab's MAIN_CONTEXT is on top
// of the stack; without this hook it would dangle on a deleted view.
func TestCloseActiveFiresOnActiveClosed(t *testing.T) {
	h, _ := newTestHelper(t, nil)
	var fired int
	h.SetOnActiveClosed(func() { fired++ })
	_ = h.openTab("a", nil)
	if err := h.CloseActive(); err != nil {
		t.Fatalf("CloseActive: %v", err)
	}
	if fired != 1 {
		t.Fatalf("onActiveClosed fired %d times, want 1", fired)
	}
}

// TestCloseActiveOnEmptyDoesNotFireOnActiveClosed: no tab closed -> no
// focus reconciliation needed.
func TestCloseActiveOnEmptyDoesNotFireOnActiveClosed(t *testing.T) {
	h, _ := newTestHelper(t, nil)
	var fired int
	h.SetOnActiveClosed(func() { fired++ })
	_ = h.CloseActive()
	if fired != 0 {
		t.Fatalf("onActiveClosed fired %d times on empty, want 0", fired)
	}
}

// TestEvictionDoesNotFireOnActiveClosed: opening past the cap evicts the
// oldest tab through the Close path, but that is NOT a user-initiated
// close of the focused tab. Reconciling focus there would steal it into
// results while the user is typing in the editor, so onActiveClosed must
// stay silent on eviction (dbsavvy-aqw).
func TestEvictionDoesNotFireOnActiveClosed(t *testing.T) {
	h, _ := newTestHelper(t, nil)
	var activeClosed, removed int
	h.SetOnActiveClosed(func() { activeClosed++ })
	h.SetOnTabRemoved(func(string) { removed++ })
	// Cap is DefaultMaxResultTabs (8); the 9th open evicts the oldest.
	for i := range DefaultMaxResultTabs + 1 {
		_ = h.openTab(fmt.Sprintf("q%d", i), nil)
	}
	if removed != 1 {
		t.Fatalf("onTabRemoved fired %d times, want 1 (eviction)", removed)
	}
	if activeClosed != 0 {
		t.Fatalf("onActiveClosed fired %d times on eviction, want 0", activeClosed)
	}
}

// --- Pin ------------------------------------------------------------------

func TestPinTogglesAndProtectsFromEviction(t *testing.T) {
	h, _ := newTestHelper(t, nil)
	tab1, _ := openAndReturn(t, h, "a")
	if pinned := h.Pin(tab1); !pinned {
		t.Fatal("Pin(tab1) returned false, want true")
	}
	if !tab1.Pinned() {
		t.Error("tab1.Pinned() = false after Pin")
	}
	if pinned := h.Pin(tab1); pinned {
		t.Error("Pin(tab1) again returned true, want false (toggle)")
	}
}

func TestPinWhileStreamRunningDoesNotDisruptState(t *testing.T) {
	factory := func() StreamRunner { return &fakeStreamRunner{} }
	h, _ := newTestHelper(t, factory)
	rh := newFakeRunHandle()
	if err := h.openTab("running", rh); err != nil {
		t.Fatalf("openTab: %v", err)
	}
	tab := h.Active()
	if tab.State() != StateRunning {
		t.Fatalf("State before Pin = %v, want Running", tab.State())
	}
	h.Pin(tab)
	if !tab.Pinned() {
		t.Error("Pinned = false")
	}
	if tab.State() != StateRunning {
		t.Errorf("State after Pin = %v, want Running (unchanged)", tab.State())
	}
}

// --- Eviction --------------------------------------------------------------

func TestEvictionDisposesOldestNonPinned(t *testing.T) {
	h, _ := newTestHelper(t, nil)
	// Fill cap (8): a..h.
	for _, sql := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		if err := h.openTab(sql, nil); err != nil {
			t.Fatalf("open %s: %v", sql, err)
		}
	}
	if h.Count() != 8 {
		t.Fatalf("Count = %d, want 8", h.Count())
	}
	// Pin slot 0 (a). Oldest non-pinned is now slot 1 (b).
	tabs := h.Tabs()
	h.Pin(tabs[0])

	// Open a 9th tab -> evicts b -> reuses slot 1.
	if err := h.openTab("i", nil); err != nil {
		t.Fatalf("openTab i: %v", err)
	}
	if h.Count() != 8 {
		t.Fatalf("after eviction Count = %d, want 8", h.Count())
	}

	// Tab "a" (pinned) must still be present.
	tabsAfter := h.Tabs()
	foundA := false
	foundB := false
	foundI := false
	for _, tab := range tabsAfter {
		switch tab.Label() {
		case "a":
			foundA = true
		case "b":
			foundB = true
		case "i":
			foundI = true
		}
	}
	if !foundA {
		t.Error("pinned tab a was evicted")
	}
	if foundB {
		t.Error("tab b should have been evicted")
	}
	if !foundI {
		t.Error("new tab i was not added")
	}
}

func TestAllPinnedAtCapRejectsOpen(t *testing.T) {
	h, toaster := newTestHelper(t, nil)
	for i := 0; i < 8; i++ {
		_ = h.openTab(fmt.Sprintf("t%d", i), nil)
	}
	for _, tab := range h.Tabs() {
		h.Pin(tab)
	}
	err := h.openTab("blocked", nil)
	if !errors.Is(err, ErrTabCapReached) {
		t.Fatalf("openTab err = %v, want ErrTabCapReached", err)
	}
	if h.Count() != 8 {
		t.Errorf("Count after rejected open = %d, want 8", h.Count())
	}
	last := toaster.Last()
	if last != "tab cap reached; unpin a tab" {
		t.Errorf("toast = %q, want 'tab cap reached; unpin a tab'", last)
	}
}

// --- Queue -----------------------------------------------------------------

func TestQueueOpensSecondTabInQueuedState(t *testing.T) {
	factory := func() StreamRunner { return &fakeStreamRunner{} }
	h, _ := newTestHelper(t, factory)

	rhA := newFakeRunHandle()
	rhB := newFakeRunHandle()

	if err := h.openTab("A", rhA); err != nil {
		t.Fatalf("open A: %v", err)
	}
	tabA := h.Active()
	if tabA.State() != StateRunning {
		t.Fatalf("A state = %v, want Running", tabA.State())
	}

	if err := h.openTab("B", rhB); err != nil {
		t.Fatalf("open B: %v", err)
	}
	tabB := h.Active()
	if tabB.State() != StateQueued {
		t.Fatalf("B state = %v, want Queued (A still running)", tabB.State())
	}

	// Finish A; B's queue waiter wakes and starts streaming.
	rhA.finish()

	// Poll for B to transition to Running. The queue waiter runs in
	// a goroutine; give it up to 200ms.
	if !waitFor(200*time.Millisecond, func() bool {
		return tabB.State() == StateRunning
	}) {
		t.Fatalf("B state after A finished = %v, want Running", tabB.State())
	}
}

func TestCancelActiveQueuedRemovesWithoutDriverCall(t *testing.T) {
	factory := func() StreamRunner { return &fakeStreamRunner{} }
	h, _ := newTestHelper(t, factory)

	rhA := newFakeRunHandle()
	rhB := newFakeRunHandle()
	_ = h.openTab("A", rhA)
	_ = h.openTab("B", rhB)
	tabB := h.Active()
	if tabB.State() != StateQueued {
		t.Fatalf("B state = %v, want Queued", tabB.State())
	}

	if err := h.CancelActive(); err != nil {
		t.Fatalf("CancelActive: %v", err)
	}
	if tabB.State() != StateCancelled {
		t.Errorf("B state after CancelActive = %v, want Cancelled", tabB.State())
	}
	// No driver-side Cancel should have fired on rhB (it was queued).
	if rhB.wasCancelled() {
		t.Error("rhB.Cancel was called; queued cancel should bypass driver")
	}
}

func TestCancelActiveRunningCallsRunHandleCancel(t *testing.T) {
	factory := func() StreamRunner { return &fakeStreamRunner{} }
	h, _ := newTestHelper(t, factory)

	rhA := newFakeRunHandle()
	_ = h.openTab("A", rhA)
	if err := h.CancelActive(); err != nil {
		t.Fatalf("CancelActive: %v", err)
	}
	if !rhA.wasCancelled() {
		t.Error("rhA.Cancel was not called for running tab")
	}
	tabA := h.Tabs()[0]
	if tabA.State() != StateCancelled {
		t.Errorf("A state after CancelActive = %v, want Cancelled", tabA.State())
	}
}

// TestCancelActiveRunningStopsRunnerToReleaseLock is the cancel-then-run
// deadlock guard (dbsavvy-dk6). Cancelling a Running tab must Stop() its
// runner, not merely rh.Cancel() it: a parked >200-row worker never
// observes the driver cancel, so only Stop() makes the worker return,
// close its stream, and release the per-session streamMu. Without the
// Stop() the lock leaks under the now-Cancelled tab and the next run
// deadlocks the UI thread on Stream.Lock() — exactly the session-2 repro.
func TestCancelActiveRunningStopsRunnerToReleaseLock(t *testing.T) {
	factory := func() StreamRunner { return &fakeStreamRunner{} }
	h, _ := newTestHelper(t, factory)

	rhA := newFakeRunHandle()
	_ = h.openTab("A", rhA)
	tabA := h.Active()
	if tabA.State() != StateRunning {
		t.Fatalf("A state = %v, want Running", tabA.State())
	}

	if err := h.CancelActive(); err != nil {
		t.Fatalf("CancelActive: %v", err)
	}

	r := tabA.runner.(*fakeStreamRunner)
	if got := r.StopCount(); got != 1 {
		t.Errorf("runner Stop count after cancel = %d, want 1 (cancel must Stop the parked worker to release streamMu)", got)
	}
	if tabA.State() != StateCancelled {
		t.Errorf("A state after cancel = %v, want Cancelled", tabA.State())
	}
}

// --- Preempt-in-flight (dbsavvy-dk6) --------------------------------------

// TestPreemptInFlightStopsRunningTab is the deadlock regression guard: a
// running stream parks its worker holding SQLSession.streamMu, so a new
// run must Stop() that worker (releasing the lock) before it acquires the
// queue. rh.Cancel() alone never releases it (a parked worker never calls
// Next), so the assertion is specifically on Stop().
func TestPreemptInFlightStopsRunningTab(t *testing.T) {
	var runners []*fakeStreamRunner
	factory := func() StreamRunner {
		r := &fakeStreamRunner{}
		runners = append(runners, r)
		return r
	}
	h, _ := newTestHelper(t, factory)

	rhA := newFakeRunHandle()
	if err := h.openTab("A", rhA); err != nil {
		t.Fatalf("openTab A: %v", err)
	}
	tabA := h.Active()
	if tabA.State() != StateRunning {
		t.Fatalf("A state = %v, want Running", tabA.State())
	}

	h.PreemptInFlight()

	if got := runners[0].StopCount(); got != 1 {
		t.Errorf("A runner Stop count = %d, want 1 (preempt must Stop the parked worker to release streamMu)", got)
	}
	if tabA.State() != StateCancelled {
		t.Errorf("A state after preempt = %v, want Cancelled", tabA.State())
	}
}

// TestPreemptInFlightAbortsQueuedTab covers the queued tab: its waiter is
// aborted without a driver-side Cancel, and the prior running tab's worker
// is stopped.
func TestPreemptInFlightAbortsQueuedTab(t *testing.T) {
	var runners []*fakeStreamRunner
	factory := func() StreamRunner {
		r := &fakeStreamRunner{}
		runners = append(runners, r)
		return r
	}
	h, _ := newTestHelper(t, factory)

	rhA := newFakeRunHandle()
	rhB := newFakeRunHandle()
	_ = h.openTab("A", rhA)
	_ = h.openTab("B", rhB) // queues behind A (A still running)
	tabB := h.Active()
	if tabB.State() != StateQueued {
		t.Fatalf("B state = %v, want Queued", tabB.State())
	}

	h.PreemptInFlight()

	if tabB.State() != StateCancelled {
		t.Errorf("B state after preempt = %v, want Cancelled", tabB.State())
	}
	if rhB.wasCancelled() {
		t.Error("queued rhB should not receive a driver Cancel")
	}
	if got := runners[0].StopCount(); got != 1 {
		t.Errorf("A runner Stop count = %d, want 1", got)
	}
}

// --- Plan / ShowError -----------------------------------------------------

func TestOpenPlanTabCreatesPlanStateTab(t *testing.T) {
	h, _ := newTestHelper(t, nil)
	plan := models.Plan{RawText: "Seq Scan on users"}
	if err := h.OpenPlanTab("EXPLAIN SELECT", plan); err != nil {
		t.Fatalf("OpenPlanTab: %v", err)
	}
	active := h.Active()
	if active == nil {
		t.Fatal("Active = nil after OpenPlanTab")
	}
	if active.State() != StatePlan {
		t.Errorf("State = %v, want Plan", active.State())
	}
	if got := active.Plan().RawText; got != "Seq Scan on users" {
		t.Errorf("Plan().RawText = %q, want 'Seq Scan on users'", got)
	}
}

func TestShowErrorCreatesErrorStateTab(t *testing.T) {
	h, _ := newTestHelper(t, nil)
	errStub := errors.New("syntax error near 'WHERE'")
	h.ShowError("SELECT bad", errStub)
	active := h.Active()
	if active == nil {
		t.Fatal("Active = nil after ShowError")
	}
	if active.State() != StateError {
		t.Errorf("State = %v, want Error", active.State())
	}
	if active.Err() != errStub {
		t.Errorf("Err = %v, want %v", active.Err(), errStub)
	}
}

// TestOpenPlanTabHasNilGrid is the regression test for dbsavvy-6pb. allocTab
// eagerly creates a grid for every tab; OpenPlanTab must clear it so Tab.Grid()
// honors its documented "nil for plan / error tabs" contract. Otherwise
// LayoutPaint's "if g := t.Grid(); g != nil { g.Render }" branch wins over the
// StatePlan branch and renders the empty grid's "(0 rows)" EmptyResultIndicator
// instead of the plan tree.
func TestOpenPlanTabHasNilGrid(t *testing.T) {
	h, _ := newTestHelper(t, nil)
	if err := h.OpenPlanTab("EXPLAIN SELECT", models.Plan{RawText: "Seq Scan on users"}); err != nil {
		t.Fatalf("OpenPlanTab: %v", err)
	}
	active := h.Active()
	if active == nil {
		t.Fatal("Active = nil after OpenPlanTab")
	}
	if g := active.Grid(); g != nil {
		t.Errorf("plan tab Grid() = %v, want nil (else LayoutPaint renders the empty grid '(0 rows)' over the plan body)", g)
	}
}

// TestShowErrorHasNilGrid is the regression test for dbsavvy-6pb (error-tab
// arm). ShowError must clear the eagerly-created grid so LayoutPaint reaches
// the Err() branch instead of rendering the empty grid's "(0 rows)".
func TestShowErrorHasNilGrid(t *testing.T) {
	h, _ := newTestHelper(t, nil)
	h.ShowError("SELECT bad", errors.New("syntax error near 'WHERE'"))
	active := h.Active()
	if active == nil {
		t.Fatal("Active = nil after ShowError")
	}
	if g := active.Grid(); g != nil {
		t.Errorf("error tab Grid() = %v, want nil (else LayoutPaint renders the empty grid '(0 rows)' over the error message)", g)
	}
}

// --- renderQueryErrorPanel (dbsavvy-fow.3) --------------------------------

// TestRenderQueryErrorPanelWithCaret asserts the full panel for a
// Position>0 syntax error: severity+code+message header, the offending
// SQL line, and a `^` caret under the offending token.
func TestRenderQueryErrorPanelWithCaret(t *testing.T) {
	qe := &drivers.QueryError{
		Raw:      errors.New(`syntax error at or near "SELET"`),
		Code:     "42601",
		Severity: "ERROR",
		Position: 1, // 1-based byte offset → caret under the first char
	}
	got := renderQueryErrorPanel(qe, "SELET 1")
	want := "ERROR 42601: syntax error at or near \"SELET\"\n\n" +
		"SELET 1\n" +
		"^"
	if got != want {
		t.Errorf("panel mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestRenderQueryErrorPanelCaretRuneBoundary asserts the caret column is
// counted in runes, not bytes: a multibyte char before the offset shifts
// the caret by one column (not its byte width), and a later-line position
// echoes the correct line.
func TestRenderQueryErrorPanelCaretRuneBoundary(t *testing.T) {
	// "é" is 2 bytes (0xC3 0xA9). SQL: "é = 1\nbad" — the 'b' of "bad" is at
	// byte offset 8 (é=2, " = 1"=4, "\n"=1 → 7; 'b' at byte 7, 1-based 8).
	sql := "é = 1\nbad"
	qe := &drivers.QueryError{
		Raw:      errors.New("boom"),
		Severity: "ERROR",
		Position: 8,
	}
	got := renderQueryErrorPanel(qe, sql)
	want := "ERROR: boom\n\nbad\n^"
	if got != want {
		t.Errorf("panel mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestRenderQueryErrorPanelNoPosition asserts Position==0 renders
// severity+message plus Detail/Hint blocks with NO caret and no panic.
func TestRenderQueryErrorPanelNoPosition(t *testing.T) {
	qe := &drivers.QueryError{
		Raw:        errors.New("duplicate key value violates unique constraint"),
		Code:       "23505",
		Severity:   "ERROR",
		Detail:     "Key (id)=(1) already exists.",
		Hint:       "Use a different id.",
		Constraint: "users_pkey",
		Position:   0,
	}
	got := renderQueryErrorPanel(qe, "INSERT INTO users VALUES (1)")
	want := "ERROR 23505: duplicate key value violates unique constraint\n\n" +
		"Detail: Key (id)=(1) already exists.\n\n" +
		"Hint: Use a different id.\n\n" +
		"Constraint: users_pkey"
	if got != want {
		t.Errorf("panel mismatch\n got: %q\nwant: %q", got, want)
	}
	if strings.Contains(got, "^") {
		t.Errorf("Position==0 panel must not contain a caret; got %q", got)
	}
}

// TestRenderQueryErrorPanelSanitizesFields asserts every server-controlled
// field is routed through grid.SanitizeCellEscapes: an ANSI CSI sequence
// in Detail and a C0 control byte in the message are stripped.
func TestRenderQueryErrorPanelSanitizesFields(t *testing.T) {
	qe := &drivers.QueryError{
		Raw:      errors.New("boom\x07with-bell"), // \x07 BEL (C0) stripped
		Severity: "ERROR",
		Detail:   "before\x1b[31mred\x1b[0mafter", // ANSI CSI stripped
	}
	got := renderQueryErrorPanel(qe, "")
	if strings.Contains(got, "\x1b") {
		t.Errorf("output must not contain ESC; got %q", got)
	}
	if strings.Contains(got, "\x07") {
		t.Errorf("output must not contain BEL; got %q", got)
	}
	want := "ERROR: boomwith-bell\n\nDetail: beforeredafter"
	if got != want {
		t.Errorf("panel mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestRenderQueryErrorPanelPositionBeyondSQL asserts an out-of-range
// Position omits the caret block (no panic).
func TestRenderQueryErrorPanelPositionBeyondSQL(t *testing.T) {
	qe := &drivers.QueryError{
		Raw:      errors.New("boom"),
		Severity: "ERROR",
		Position: 999,
	}
	got := renderQueryErrorPanel(qe, "SELECT 1")
	want := "ERROR: boom"
	if got != want {
		t.Errorf("panel mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestRenderQueryErrorPanelDefaultSeverity asserts a missing Severity
// defaults to "ERROR" so the header is never code-less.
func TestRenderQueryErrorPanelDefaultSeverity(t *testing.T) {
	qe := &drivers.QueryError{Raw: errors.New("boom")}
	got := renderQueryErrorPanel(qe, "")
	if want := "ERROR: boom"; got != want {
		t.Errorf("panel = %q, want %q", got, want)
	}
}

// TestAttachActiveTabErrorSQL asserts the error SQL is recorded on the
// active error tab so the render path can reach it for the caret.
func TestAttachActiveTabErrorSQL(t *testing.T) {
	h, _ := newTestHelper(t, nil)
	h.ShowError("SELET 1", &drivers.QueryError{Raw: errors.New("syntax"), Position: 1})
	h.AttachActiveTabErrorSQL("SELET 1")
	active := h.Active()
	if active == nil {
		t.Fatal("Active = nil after ShowError")
	}
	if got := active.errSQLSnapshot(); got != "SELET 1" {
		t.Errorf("errSQLSnapshot = %q, want %q", got, "SELET 1")
	}
}

// --- Title ----------------------------------------------------------------

func TestTabTitleFormat(t *testing.T) {
	h, _ := newTestHelper(t, nil)
	_ = h.openTab("SELECT * FROM users", nil)
	active := h.Active()
	// The frame title carries only metadata (the tab bar shows the
	// query); in-flight tabs prefix the row count with "~" to signal an
	// approximate (still-streaming) value.
	want := "~0 rows · running"
	if got := active.Title(); got != want {
		t.Errorf("Title = %q, want %q", got, want)
	}
}

// --- Two rapid opens -----------------------------------------------------

func TestTwoRapidOpensOnlyOneStreamInFlight(t *testing.T) {
	var startedRunners atomic.Int32
	factory := func() StreamRunner {
		startedRunners.Add(1)
		return &fakeStreamRunner{}
	}
	h, _ := newTestHelper(t, factory)
	rhA := newFakeRunHandle()
	rhB := newFakeRunHandle()
	_ = h.openTab("A", rhA)
	_ = h.openTab("B", rhB)

	tabs := h.Tabs()
	if tabs[0].State() != StateRunning || tabs[1].State() != StateQueued {
		t.Errorf("states = %v, %v; want Running, Queued", tabs[0].State(), tabs[1].State())
	}
	// Both runners are allocated (one per tab) but only A.runner has
	// received NewQueryTask. B.runner is idle until A.Done fires.
	if startedRunners.Load() != 2 {
		t.Errorf("startedRunners = %d, want 2 (one per tab)", startedRunners.Load())
	}
	runnerA := tabs[0].runner.(*fakeStreamRunner)
	runnerB := tabs[1].runner.(*fakeStreamRunner)
	if runnerA.StartCount() != 1 {
		t.Errorf("runnerA.starts = %d, want 1", runnerA.StartCount())
	}
	if runnerB.StartCount() != 0 {
		t.Errorf("runnerB.starts = %d, want 0 (queued)", runnerB.StartCount())
	}

	// Drain A so B's queue wakes; runnerB.starts becomes 1.
	rhA.finish()
	if !waitFor(200*time.Millisecond, func() bool {
		return runnerB.StartCount() == 1
	}) {
		t.Errorf("runnerB.starts after A finished = %d, want 1", runnerB.StartCount())
	}
}

// --- Close on a running tab disposes synchronously -----------------------

func TestCloseRunningTabDisposesStream(t *testing.T) {
	factory := func() StreamRunner { return &fakeStreamRunner{} }
	h, _ := newTestHelper(t, factory)
	rh := newFakeRunHandle()
	_ = h.openTab("A", rh)
	tab := h.Active()

	if err := h.Close(tab); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !rh.wasCancelled() {
		t.Error("rh.Cancel was not called on Close of running tab")
	}
	r := tab.runner.(*fakeStreamRunner)
	if r.StopCount() != 1 {
		t.Errorf("runner.Stop count = %d, want 1", r.StopCount())
	}
	if h.Count() != 0 {
		t.Errorf("Count after Close = %d, want 0", h.Count())
	}
}

// --- dbsavvy-uv0.3: prefetch wiring, paging, ReadToEnd, complete flag ----

// fakeConfirmer records Confirm calls and lets the test drive the
// onYes / onNo callbacks deterministically.
type fakeConfirmer struct {
	mu       sync.Mutex
	calls    int
	lastYes  func() error
	lastNo   func() error
	autoYes  bool
	autoNo   bool
	lastBody string
}

func (f *fakeConfirmer) Confirm(title, body string, onYes, onNo func() error) error {
	f.mu.Lock()
	f.calls++
	f.lastYes = onYes
	f.lastNo = onNo
	f.lastBody = body
	autoYes := f.autoYes
	autoNo := f.autoNo
	f.mu.Unlock()
	_ = title
	if autoYes && onYes != nil {
		return onYes()
	}
	if autoNo && onNo != nil {
		return onNo()
	}
	return nil
}

func (f *fakeConfirmer) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestSetOnNearTailWiringFiresReadRowsOnCursorCross verifies that when
// the grid cursor enters the near-tail prefetch window, the helper-
// installed callback invokes runner.ReadRows exactly once with the
// configured prefetch row count (grid.ResultPrefetchRows = 50).
//
// dbsavvy-uv0.3 AC #1.
func TestSetOnNearTailWiringFiresReadRowsOnCursorCross(t *testing.T) {
	runner := &fakeStreamRunner{}
	factory := func() StreamRunner { return runner }
	h, _ := newTestHelper(t, factory)

	rh := newFakeRunHandle()
	if err := h.openTab("Q", rh); err != nil {
		t.Fatalf("openTab: %v", err)
	}
	tab := h.Active()
	g := tab.Grid()

	g.SetColumns([]models.ColumnMeta{{Name: "c0", TypeName: "text"}})
	// Append 30 rows so PrefetchThreshold=25 is crossed near the tail.
	rows := make([]models.Row, 30)
	for i := range rows {
		rows[i] = models.Row{Values: []any{i}}
	}
	g.AppendRows(rows)

	// Drive cursor into the near-tail zone via Render-triggered checks.
	for i := 0; i < 28; i++ {
		g.MoveCursorDown()
		g.Render(nil) // nil target is allowed
	}

	calls := runner.ReadRowsCalls()
	if len(calls) != 1 {
		t.Fatalf("ReadRows calls = %v, want exactly 1", calls)
	}
	// The wired prefetch payload is grid.ResultPrefetchRows (50).
	if calls[0] != 50 {
		t.Errorf("ReadRows arg = %d, want 50", calls[0])
	}
}

// TestPrefetchDoesNotDoubleFireForSameRowsLen verifies the
// lastNearTailFireAt gate: scrolling around inside the near-tail window
// fires exactly once per rows-length crossing. dbsavvy-uv0.3 AC #5.
func TestPrefetchDoesNotDoubleFireForSameRowsLen(t *testing.T) {
	runner := &fakeStreamRunner{}
	factory := func() StreamRunner { return runner }
	h, _ := newTestHelper(t, factory)

	rh := newFakeRunHandle()
	_ = h.openTab("Q", rh)
	tab := h.Active()
	g := tab.Grid()

	g.SetColumns([]models.ColumnMeta{{Name: "c0", TypeName: "text"}})
	rows := make([]models.Row, 30)
	for i := range rows {
		rows[i] = models.Row{Values: []any{i}}
	}
	g.AppendRows(rows)

	// Cross the threshold the first time.
	for i := 0; i < 28; i++ {
		g.MoveCursorDown()
		g.Render(nil)
	}
	// Bounce out and back in WITHOUT growing rows.
	for i := 0; i < 10; i++ {
		g.MoveCursorUp()
	}
	g.Render(nil)
	for i := 0; i < 10; i++ {
		g.MoveCursorDown()
		g.Render(nil)
	}
	calls := runner.ReadRowsCalls()
	if len(calls) != 1 {
		t.Errorf("ReadRows fired %d times for identical rowsLen; want exactly 1", len(calls))
	}
}

// TestPagePlusOneRequestsReadRowsAndJumpsLast verifies that ]p (Page(+1))
// fires runner.ReadRows(pageSize) AND jumps the grid cursor to the
// loaded tail. dbsavvy-uv0.3 AC #2.
func TestPagePlusOneRequestsReadRowsAndJumpsLast(t *testing.T) {
	runner := &fakeStreamRunner{}
	factory := func() StreamRunner { return runner }
	h, _ := newTestHelper(t, factory)

	rh := newFakeRunHandle()
	_ = h.openTab("Q", rh)
	tab := h.Active()
	g := tab.Grid()
	g.SetColumns([]models.ColumnMeta{{Name: "c0", TypeName: "text"}})
	rows := make([]models.Row, 10)
	for i := range rows {
		rows[i] = models.Row{Values: []any{i}}
	}
	g.AppendRows(rows)

	runner.readRowsCalls = nil // reset any prefetch fires (none expected here)

	h.Page(1)

	calls := runner.ReadRowsCalls()
	if len(calls) != 1 {
		t.Fatalf("Page(+1) ReadRows calls = %v, want exactly 1", calls)
	}
	// Default ResultPageSize wiring = 200 (grid.ResultPageSize).
	if calls[0] != 200 {
		t.Errorf("Page(+1) requested %d rows, want 200", calls[0])
	}
	row, _ := g.CursorPosition()
	if row != 9 {
		t.Errorf("cursor row after Page(+1) = %d, want 9 (last loaded)", row)
	}
}

// TestJumpLastMovesCursorToLastRowInGridMode guards dbsavvy-6t9: in the
// default grid view, JumpLast (G) moves the cursor to the last loaded
// row. The stream is marked complete first, so the old G->ReadToEnd
// wiring would no-op and leave the cursor at row 0 — this test would
// fail under that regression.
func TestJumpLastMovesCursorToLastRowInGridMode(t *testing.T) {
	runner := &fakeStreamRunner{}
	factory := func() StreamRunner { return runner }
	h, _ := newTestHelper(t, factory)

	rh := newFakeRunHandle()
	_ = h.openTab("Q", rh)
	tab := h.Active()
	g := tab.Grid()
	g.SetColumns([]models.ColumnMeta{{Name: "c0", TypeName: "text"}})
	rows := make([]models.Row, 10)
	for i := range rows {
		rows[i] = models.Row{Values: []any{i}}
	}
	g.AppendRows(rows)
	runner.fireOnDone() // mark complete: old ReadToEnd path would no-op here
	if !tab.Complete() {
		t.Fatalf("tab not marked complete")
	}
	if startRow, _ := g.CursorPosition(); startRow != 0 {
		t.Fatalf("cursor should start at row 0, got %d", startRow)
	}

	h.JumpLast()

	if row, _ := g.CursorPosition(); row != 9 {
		t.Errorf("cursor row after JumpLast = %d, want 9 (last loaded)", row)
	}
}

// TestPageMinusOneRewindsCursor verifies [p (Page(-1)) rewinds the
// cursor (anchored at the top) and does NOT fire ReadRows.
// dbsavvy-uv0.3 AC #2.
func TestPageMinusOneRewindsCursor(t *testing.T) {
	runner := &fakeStreamRunner{}
	factory := func() StreamRunner { return runner }
	h, _ := newTestHelper(t, factory)

	rh := newFakeRunHandle()
	_ = h.openTab("Q", rh)
	tab := h.Active()
	g := tab.Grid()
	g.SetColumns([]models.ColumnMeta{{Name: "c0", TypeName: "text"}})
	rows := make([]models.Row, 250)
	for i := range rows {
		rows[i] = models.Row{Values: []any{i}}
	}
	g.AppendRows(rows)
	// Park cursor near the tail.
	for i := 0; i < 240; i++ {
		g.MoveCursorDown()
	}
	startRow, _ := g.CursorPosition()
	runner.readRowsCalls = nil

	h.Page(-1)

	endRow, _ := g.CursorPosition()
	if endRow >= startRow {
		t.Errorf("Page(-1) did not move cursor up: %d -> %d", startRow, endRow)
	}
	if len(runner.ReadRowsCalls()) != 0 {
		t.Errorf("Page(-1) fired ReadRows; want no fetch on rewind")
	}
}

// TestPagePlusOneOnCompleteStreamIsNoop verifies the AC "]p when stream
// is already complete: no-op (no ReadRows)". dbsavvy-uv0.3.
func TestPagePlusOneOnCompleteStreamIsNoop(t *testing.T) {
	runner := &fakeStreamRunner{}
	factory := func() StreamRunner { return runner }
	h, _ := newTestHelper(t, factory)

	rh := newFakeRunHandle()
	_ = h.openTab("Q", rh)
	tab := h.Active()
	// Simulate stream EOF: fire the registered onDone (which marshals
	// through OnUIThread = nil = synchronous).
	runner.fireOnDone()
	if !tab.Complete() {
		t.Fatalf("tab not marked complete after fireOnDone")
	}
	runner.readRowsCalls = nil

	h.Page(1)
	if calls := runner.ReadRowsCalls(); len(calls) != 0 {
		t.Errorf("Page(+1) on complete stream fired ReadRows %v; want no-op", calls)
	}
}

// TestReadToEndBelowThresholdFiresWithoutPrompt verifies the AC "G with
// EstimatedRows ≤ threshold fires without prompt". dbsavvy-uv0.3 AC #3.
func TestReadToEndBelowThresholdFiresWithoutPrompt(t *testing.T) {
	runner := &fakeStreamRunner{}
	runner.setEstimatedRows(500)
	confirm := &fakeConfirmer{}
	factory := func() StreamRunner { return runner }
	deps := ResultTabsHelperDeps{
		Toast:                  &fakeToaster{},
		StreamFactory:          factory,
		Now:                    time.Now,
		Confirm:                confirm,
		ReadToEndWarnThreshold: 1_000_000,
	}
	h := NewResultTabsHelper(deps)

	rh := newFakeRunHandle()
	_ = h.openTab("Q", rh)

	h.ReadToEnd()

	if confirm.Calls() != 0 {
		t.Errorf("Confirm calls = %d, want 0 (below threshold)", confirm.Calls())
	}
	if runner.ReadToEndCount() != 1 {
		t.Errorf("runner.ReadToEnd called %d times, want 1", runner.ReadToEndCount())
	}
}

// TestReadToEndAboveThresholdPromptsThenFiresOnYes verifies the AC
// "G above threshold: prompt first; only <CR> proceeds".
// dbsavvy-uv0.3 AC #3.
func TestReadToEndAboveThresholdPromptsThenFiresOnYes(t *testing.T) {
	runner := &fakeStreamRunner{}
	runner.setEstimatedRows(2_000_000)
	confirm := &fakeConfirmer{autoYes: true}
	factory := func() StreamRunner { return runner }
	deps := ResultTabsHelperDeps{
		Toast:                  &fakeToaster{},
		StreamFactory:          factory,
		Now:                    time.Now,
		Confirm:                confirm,
		ReadToEndWarnThreshold: 1_000_000,
	}
	h := NewResultTabsHelper(deps)
	_ = h.openTab("Q", newFakeRunHandle())

	h.ReadToEnd()

	if confirm.Calls() != 1 {
		t.Errorf("Confirm calls = %d, want 1", confirm.Calls())
	}
	if runner.ReadToEndCount() != 1 {
		t.Errorf("runner.ReadToEnd called %d times after autoYes, want 1", runner.ReadToEndCount())
	}
}

// TestReadToEndAboveThresholdNoFireOnDismiss verifies the AC "User
// dismisses G prompt with <esc>: incomplete state, no ReadRows fired".
// dbsavvy-uv0.3 edge case.
func TestReadToEndAboveThresholdNoFireOnDismiss(t *testing.T) {
	runner := &fakeStreamRunner{}
	runner.setEstimatedRows(2_000_000)
	confirm := &fakeConfirmer{autoNo: true}
	factory := func() StreamRunner { return runner }
	deps := ResultTabsHelperDeps{
		Toast:                  &fakeToaster{},
		StreamFactory:          factory,
		Now:                    time.Now,
		Confirm:                confirm,
		ReadToEndWarnThreshold: 1_000_000,
	}
	h := NewResultTabsHelper(deps)
	tab, _ := openAndReturnRH(t, h, "Q")

	h.ReadToEnd()

	if runner.ReadToEndCount() != 0 {
		t.Errorf("runner.ReadToEnd fired %d times after dismiss, want 0", runner.ReadToEndCount())
	}
	if tab.Complete() {
		t.Error("tab marked complete despite dismissed prompt")
	}
}

// TestReadToEndUnknownEstimatePrompts verifies the tiebreaker:
// "!complete && EstimatedRows.Load() == 0: G shows prompt".
// dbsavvy-uv0.3.
func TestReadToEndUnknownEstimatePrompts(t *testing.T) {
	runner := &fakeStreamRunner{} // EstimatedRows() == 0
	confirm := &fakeConfirmer{}
	factory := func() StreamRunner { return runner }
	deps := ResultTabsHelperDeps{
		Toast:                  &fakeToaster{},
		StreamFactory:          factory,
		Now:                    time.Now,
		Confirm:                confirm,
		ReadToEndWarnThreshold: 1_000_000,
	}
	h := NewResultTabsHelper(deps)
	_ = h.openTab("Q", newFakeRunHandle())

	h.ReadToEnd()

	if confirm.Calls() != 1 {
		t.Errorf("Confirm calls = %d, want 1 (unknown estimate = conservative)", confirm.Calls())
	}
	if runner.ReadToEndCount() != 0 {
		t.Errorf("runner.ReadToEnd fired %d times before user accepts, want 0", runner.ReadToEndCount())
	}
}

// TestReadToEndOnEmptyCompleteIsNoop verifies the AC "Empty result
// (0 rows, complete=true): G is a no-op". dbsavvy-uv0.3.
func TestReadToEndOnEmptyCompleteIsNoop(t *testing.T) {
	runner := &fakeStreamRunner{}
	confirm := &fakeConfirmer{}
	factory := func() StreamRunner { return runner }
	deps := ResultTabsHelperDeps{
		Toast:                  &fakeToaster{},
		StreamFactory:          factory,
		Now:                    time.Now,
		Confirm:                confirm,
		ReadToEndWarnThreshold: 1_000_000,
	}
	h := NewResultTabsHelper(deps)
	_ = h.openTab("Q", newFakeRunHandle())

	// Flip the tab into the complete-with-zero-rows state.
	runner.fireOnDone()

	h.ReadToEnd()

	if confirm.Calls() != 0 {
		t.Errorf("Confirm calls = %d, want 0 (empty complete tab)", confirm.Calls())
	}
	if runner.ReadToEndCount() != 0 {
		t.Errorf("runner.ReadToEnd fired %d times on empty-complete tab, want 0", runner.ReadToEndCount())
	}
}

// TestTabCompleteFlagDropsTilde verifies that completion drops the "~"
// approximate-count prefix and the trailing state segment, leaving just
// the final row count (the tab-bar glyph conveys the completed state).
func TestTabCompleteFlagDropsTilde(t *testing.T) {
	runner := &fakeStreamRunner{}
	factory := func() StreamRunner { return runner }
	h, _ := newTestHelper(t, factory)
	_ = h.openTab("SELECT 1", newFakeRunHandle())
	tab := h.Active()

	// Before complete: title carries "~N rows · running".
	pre := tab.Title()
	if !contains(pre, "~0 rows") {
		t.Errorf("pre-complete title %q missing ~N rows prefix", pre)
	}

	// Fire the registered onDone to mark complete. Since OnUIThread is
	// nil, the flip runs synchronously.
	runner.fireOnDone()
	if !tab.Complete() {
		t.Fatal("tab not marked complete after fireOnDone")
	}
	post := tab.Title()
	if post != "0 rows" {
		t.Errorf("post-complete title = %q, want %q", post, "0 rows")
	}
}

// TestCompleteFlipMarshalsThroughOnUIThread verifies the AC "complete
// flip marshals through onUIThread (assert callback was invoked, no
// direct write off-thread)". Race-test target. dbsavvy-uv0.3 AC #4.
func TestCompleteFlipMarshalsThroughOnUIThread(t *testing.T) {
	runner := &fakeStreamRunner{}
	factory := func() StreamRunner { return runner }
	var uiCalls atomic.Int32
	uiCh := make(chan func() error, 16)
	deps := ResultTabsHelperDeps{
		Toast:         &fakeToaster{},
		StreamFactory: factory,
		Now:           time.Now,
		OnUIThread: func(fn func() error) {
			uiCalls.Add(1)
			uiCh <- fn
		},
	}
	h := NewResultTabsHelper(deps)
	_ = h.openTab("Q", newFakeRunHandle())
	tab := h.Active()

	// onDone fires on a worker goroutine; the flip must NOT happen
	// inline — it must be enqueued via OnUIThread.
	done := make(chan struct{})
	go func() {
		runner.fireOnDone()
		close(done)
	}()
	<-done

	// At this point the worker has enqueued the flip but has NOT yet
	// executed it. tab.complete should still be false.
	if tab.Complete() {
		t.Error("tab.complete flipped without OnUIThread draining")
	}
	if uiCalls.Load() == 0 {
		t.Fatal("OnUIThread was never invoked; flip did not marshal")
	}
	// Drain the queue (simulates the gocui MainLoop running).
	close(uiCh)
	for fn := range uiCh {
		_ = fn()
	}
	if !tab.Complete() {
		t.Error("tab.complete still false after draining OnUIThread queue")
	}
}

// TestPrefetchAtRow0WithBufferLargerThanThresholdNoFire verifies the AC
// "Cursor at row 0 with 200 rows loaded: prefetch does NOT fire".
// dbsavvy-uv0.3 edge case.
func TestPrefetchAtRow0WithBufferLargerThanThresholdNoFire(t *testing.T) {
	runner := &fakeStreamRunner{}
	factory := func() StreamRunner { return runner }
	h, _ := newTestHelper(t, factory)
	_ = h.openTab("Q", newFakeRunHandle())
	tab := h.Active()
	g := tab.Grid()
	g.SetColumns([]models.ColumnMeta{{Name: "c0", TypeName: "text"}})
	rows := make([]models.Row, 200)
	for i := range rows {
		rows[i] = models.Row{Values: []any{i}}
	}
	g.AppendRows(rows)

	// Cursor at row 0; far from tail (rowsLen-cursorRow = 200 > 25).
	g.Render(nil)

	if calls := runner.ReadRowsCalls(); len(calls) != 0 {
		t.Errorf("prefetch fired at cursor=0 with 200 rows loaded: %v", calls)
	}
}

// openAndReturnRH opens a tab with a real fakeRunHandle and returns the
// active tab. Distinct from openAndReturn (which passes nil rh).
func openAndReturnRH(t *testing.T, h *ResultTabsHelper, label string) (*Tab, error) {
	t.Helper()
	if err := h.openTab(label, newFakeRunHandle()); err != nil {
		return nil, err
	}
	return h.Active(), nil
}

// --- Helpers -------------------------------------------------------------

func openAndReturn(t *testing.T, h *ResultTabsHelper, label string) (*Tab, error) {
	t.Helper()
	if err := h.openTab(label, nil); err != nil {
		return nil, err
	}
	return h.Active(), nil
}

// waitFor polls cond until it returns true or timeout elapses. Returns
// the cond outcome at exit.
func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return cond()
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// --- dbsavvy-uv0.4 /regex filter tests -----------------------------------

// fakeSearcher captures the SearchLineOpts handed to Open so a test can
// drive the OnChange / OnCancel seams directly (the live incremental
// path the master-editor onChange hook would otherwise fire).
type fakeSearcher struct {
	mu   sync.Mutex
	opts SearchLineOpts
}

func (f *fakeSearcher) Open(opts SearchLineOpts) error {
	f.mu.Lock()
	f.opts = opts
	f.mu.Unlock()
	return nil
}

// typeQuery simulates the user typing query into the search input: it
// invokes the captured OnChange seam (drives g.SetSearch live).
func (f *fakeSearcher) typeQuery(query string) {
	f.mu.Lock()
	cb := f.opts.OnChange
	f.mu.Unlock()
	if cb != nil {
		cb(query)
	}
}

// cancel simulates <esc> while composing: invokes the captured OnCancel
// seam (clears the search). The cursor-restore closure is driven by the
// helper in production; here OnCancel alone is enough to assert
// search-cleared semantics.
func (f *fakeSearcher) cancel() {
	f.mu.Lock()
	cb := f.opts.OnCancel
	f.mu.Unlock()
	if cb != nil {
		cb()
	}
}

func newSearchTestHelper(t *testing.T, searcher *fakeSearcher) (*ResultTabsHelper, *fakeToaster) {
	t.Helper()
	toaster := &fakeToaster{}
	deps := ResultTabsHelperDeps{
		Toast:   toaster,
		Search:  searcher,
		MaxTabs: 0,
		Now:     time.Now,
	}
	return NewResultTabsHelper(deps), toaster
}

// TestTabCaveatShown_ResetOnStartStreaming verifies that flipping
// caveatShown true and then re-attaching via startStreaming resets it
// back to false. This is the rerun-in-same-tab path: re-running a SELECT
// should re-fire the caveat.
func TestTabCaveatShown_ResetOnStartStreaming(t *testing.T) {
	h, _ := newSearchTestHelper(t, &fakeSearcher{})
	if err := h.openTab("Q", nil); err != nil {
		t.Fatalf("openTab: %v", err)
	}
	tab := h.Active()
	if tab == nil {
		t.Fatal("no active tab after openTab")
	}
	tab.SetCaveatShown(true)
	if !tab.CaveatShown() {
		t.Fatal("SetCaveatShown(true) did not stick")
	}
	// startStreaming is the helper's fresh-schema-attach hook.
	h.startStreaming(tab)
	if tab.CaveatShown() {
		t.Error("startStreaming must reset caveatShown")
	}
}

// TestSearchPrompt_AppliesLiveAndFiresCaveatOnce verifies the chord-
// handler behavior: opening search on an incomplete tab does NOT fire the
// caveat until the first non-empty query; that first non-empty keystroke
// fires it once and flips caveatShown; a second non-empty keystroke does
// not re-toast; typing drives live SetSearch.
func TestSearchPrompt_AppliesLiveAndFiresCaveatOnce(t *testing.T) {
	searcher := &fakeSearcher{}
	h, toaster := newSearchTestHelper(t, searcher)
	if err := h.openTab("Q", nil); err != nil {
		t.Fatalf("openTab: %v", err)
	}
	tab := h.Active()
	// Install columns so the grid has a schema; tab is incomplete by default.
	tab.Grid().SetColumns([]models.ColumnMeta{{Name: "c", TypeName: "text"}})
	tab.Grid().AppendRows([]models.Row{{Values: []any{"alice"}}})

	caveatCount := func() int {
		n := 0
		for _, m := range toaster.Messages() {
			if contains(m, "searching loaded rows only") {
				n++
			}
		}
		return n
	}

	// Opening search and doing nothing must NOT fire the caveat.
	h.SearchPrompt()
	if tab.CaveatShown() {
		t.Error("open with no keystroke → caveatShown must stay false")
	}
	if caveatCount() != 0 {
		t.Errorf("open with no keystroke → no caveat toast; got %v", toaster.Messages())
	}

	// An empty-query keystroke alone must NOT fire the caveat.
	searcher.typeQuery("")
	if tab.CaveatShown() || caveatCount() != 0 {
		t.Errorf("empty query → no caveat; shown=%v msgs=%v", tab.CaveatShown(), toaster.Messages())
	}

	// First non-empty query: caveat fires exactly once and live search installs.
	searcher.typeQuery("alic")
	if !tab.Grid().SearchActive() {
		t.Error("non-empty query must install live search")
	}
	if !tab.CaveatShown() {
		t.Error("first non-empty query on incomplete tab → caveatShown should flip true")
	}
	if caveatCount() != 1 {
		t.Errorf("first non-empty query → exactly one caveat toast; got %v", toaster.Messages())
	}

	// Second non-empty query on the same incomplete tab: caveat must NOT re-fire.
	searcher.typeQuery("bob")
	if caveatCount() != 1 {
		t.Errorf("caveat must fire once per tab; got %d caveat toasts (msgs=%v)", caveatCount(), toaster.Messages())
	}
}

// TestSearchPrompt_EmptyQueryInactive verifies an empty typed query
// leaves the search inactive (no highlights).
func TestSearchPrompt_EmptyQueryInactive(t *testing.T) {
	searcher := &fakeSearcher{}
	h, _ := newSearchTestHelper(t, searcher)
	if err := h.openTab("Q", nil); err != nil {
		t.Fatalf("openTab: %v", err)
	}
	tab := h.Active()
	tab.Grid().SetColumns([]models.ColumnMeta{{Name: "c", TypeName: "text"}})
	tab.Grid().AppendRows([]models.Row{{Values: []any{"alice"}}})

	h.SearchPrompt()
	searcher.typeQuery("")
	if tab.Grid().SearchActive() {
		t.Error("empty query must leave search inactive")
	}
}

// TestSearchPrompt_CancelClearsSearch verifies <esc> while composing
// clears the active search via the OnCancel seam.
func TestSearchPrompt_CancelClearsSearch(t *testing.T) {
	searcher := &fakeSearcher{}
	h, _ := newSearchTestHelper(t, searcher)
	if err := h.openTab("Q", nil); err != nil {
		t.Fatalf("openTab: %v", err)
	}
	tab := h.Active()
	tab.Grid().SetColumns([]models.ColumnMeta{{Name: "c", TypeName: "text"}})
	tab.Grid().AppendRows([]models.Row{{Values: []any{"alice"}}})

	h.SearchPrompt()
	searcher.typeQuery("alic")
	if !tab.Grid().SearchActive() {
		t.Fatal("precondition: search should be active after typing")
	}
	searcher.cancel()
	if tab.Grid().SearchActive() {
		t.Error("OnCancel must clear the active search")
	}
}

// TestSearchActive_FalseWithoutSearch verifies <esc>-gating behavior:
// SearchActive returns false when no search is installed.
func TestSearchActive_FalseWithoutSearch(t *testing.T) {
	h, _ := newSearchTestHelper(t, &fakeSearcher{})
	if err := h.openTab("Q", nil); err != nil {
		t.Fatalf("openTab: %v", err)
	}
	if h.SearchActive() {
		t.Error("SearchActive should be false on a fresh tab")
	}
}

// --- dbsavvy-uv0.5 sort picker tests -------------------------------------

// fakeChooser captures Choose invocations and exposes hooks for the test
// to submit / cancel a specific index.
type fakeChooser struct {
	mu         sync.Mutex
	lastLabel  string
	lastChoice []string
	onSubmit   func(idx int) error
	onCancel   func() error
	calls      int
}

func (f *fakeChooser) Choose(label string, choices []string, onSubmit func(idx int) error, onCancel func() error) error {
	f.mu.Lock()
	f.lastLabel = label
	f.lastChoice = append([]string(nil), choices...)
	f.onSubmit = onSubmit
	f.onCancel = onCancel
	f.calls++
	f.mu.Unlock()
	return nil
}

func (f *fakeChooser) submit(idx int) error {
	f.mu.Lock()
	cb := f.onSubmit
	f.mu.Unlock()
	if cb == nil {
		return nil
	}
	return cb(idx)
}

func (f *fakeChooser) cancel() error {
	f.mu.Lock()
	cb := f.onCancel
	f.mu.Unlock()
	if cb == nil {
		return nil
	}
	return cb()
}

func newSortTestHelper(t *testing.T, chooser *fakeChooser) (*ResultTabsHelper, *fakeToaster) {
	t.Helper()
	toaster := &fakeToaster{}
	deps := ResultTabsHelperDeps{
		Toast:         toaster,
		Choice:        chooser,
		Now:           time.Now,
		SortPickLabel: "sort by column",
	}
	return NewResultTabsHelper(deps), toaster
}

// TestSortPick_OpensPickerWithGridColumns pins: SortPick passes the
// grid's column names to the chooser, in column order.
func TestSortPick_OpensPickerWithGridColumns(t *testing.T) {
	chooser := &fakeChooser{}
	h, _ := newSortTestHelper(t, chooser)
	if err := h.openTab("Q", nil); err != nil {
		t.Fatalf("openTab: %v", err)
	}
	tab := h.Active()
	tab.Grid().SetColumns([]models.ColumnMeta{
		{Name: "name", TypeName: "text"},
		{Name: "age", TypeName: "int4"},
	})

	h.SortPick()

	chooser.mu.Lock()
	defer chooser.mu.Unlock()
	if chooser.calls != 1 {
		t.Errorf("Choose calls = %d; want 1", chooser.calls)
	}
	if chooser.lastLabel != "sort by column" {
		t.Errorf("label = %q; want %q", chooser.lastLabel, "sort by column")
	}
	if len(chooser.lastChoice) != 2 || chooser.lastChoice[0] != "name" || chooser.lastChoice[1] != "age" {
		t.Errorf("choices = %v; want [name age]", chooser.lastChoice)
	}
}

// TestSortPick_SubmitFiresOnSortRequest pins (dbsavvy-72k.5): submitting
// an index from the picker routes the RAW column index into the wired
// onSortRequest sink (the Tab-level flow) — it no longer calls
// grid.SetSort directly, so the grid's own sort state stays untouched.
func TestSortPick_SubmitFiresOnSortRequest(t *testing.T) {
	chooser := &fakeChooser{}
	h, _ := newSortTestHelper(t, chooser)
	var got []int
	h.SetOnSortRequest(func(col int) { got = append(got, col) })
	if err := h.openTab("Q", nil); err != nil {
		t.Fatalf("openTab: %v", err)
	}
	tab := h.Active()
	tab.Grid().SetColumns([]models.ColumnMeta{
		{Name: "name", TypeName: "text"},
		{Name: "age", TypeName: "int4"},
	})

	h.SortPick()
	if err := chooser.submit(1); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(got) != 1 || got[0] != 1 {
		t.Errorf("onSortRequest calls = %v; want [1] (raw col index)", got)
	}
	if tab.Grid().SortActive() {
		t.Error("SortPick must NOT call grid.SetSort directly anymore")
	}
}

// TestSortPick_SubmitNoSinkIsNoOp pins: with no onSortRequest wired,
// submitting the picker is a silent no-op (no panic, no grid SetSort) —
// matching the no-op-when-unwired behavior of the rest of SortPick.
func TestSortPick_SubmitNoSinkIsNoOp(t *testing.T) {
	chooser := &fakeChooser{}
	h, _ := newSortTestHelper(t, chooser)
	if err := h.openTab("Q", nil); err != nil {
		t.Fatalf("openTab: %v", err)
	}
	tab := h.Active()
	tab.Grid().SetColumns([]models.ColumnMeta{{Name: "name", TypeName: "text"}})

	h.SortPick()
	if err := chooser.submit(0); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if tab.Grid().SortActive() {
		t.Error("submit with no sink must not activate sort")
	}
}

// TestSortPick_RawIndexUnderHiddenColumns pins AC rule 3: the picker
// feeds RAW gridColumnNames indices so a hidden column cannot shift the
// ordinal handed downstream. gridColumnNames walks 0..ColumnCount over
// raw v.cols, and the header path (headerColumnAt) likewise returns a raw
// v.cols index — both keep col+1 stable regardless of hide state.
func TestSortPick_RawIndexUnderHiddenColumns(t *testing.T) {
	chooser := &fakeChooser{}
	h, _ := newSortTestHelper(t, chooser)
	var got []int
	h.SetOnSortRequest(func(col int) { got = append(got, col) })
	if err := h.openTab("Q", nil); err != nil {
		t.Fatalf("openTab: %v", err)
	}
	tab := h.Active()
	tab.Grid().SetColumns([]models.ColumnMeta{
		{Name: "a", TypeName: "text"},
		{Name: "b", TypeName: "text"},
		{Name: "c", TypeName: "text"},
	})
	// Hide the middle column. Even with col 1 hidden, picking the third
	// column from the picker must surface its RAW index (2), not a
	// visible-order index (1).
	tab.Grid().SetHiddenCols(map[int]bool{1: true})

	h.SortPick()
	if err := chooser.submit(2); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(got) != 1 || got[0] != 2 {
		t.Errorf("onSortRequest calls = %v; want [2] (raw index unaffected by hidden col)", got)
	}
}

// TestSortPick_CancelLeavesStateUnchanged pins AC: <esc> on the picker
// closes without touching sort state.
func TestSortPick_CancelLeavesStateUnchanged(t *testing.T) {
	chooser := &fakeChooser{}
	h, _ := newSortTestHelper(t, chooser)
	if err := h.openTab("Q", nil); err != nil {
		t.Fatalf("openTab: %v", err)
	}
	tab := h.Active()
	tab.Grid().SetColumns([]models.ColumnMeta{{Name: "n", TypeName: "text"}})

	h.SortPick()
	if err := chooser.cancel(); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if tab.Grid().SortActive() {
		t.Error("cancel must NOT activate sort")
	}
}

// TestSortPick_NoTabsToasts pins: SortPick with no tabs surfaces the
// "no result tabs" toast (matches SearchPrompt behavior).
func TestSortPick_NoTabsToasts(t *testing.T) {
	chooser := &fakeChooser{}
	h, toaster := newSortTestHelper(t, chooser)
	h.SortPick()
	msgs := toaster.Messages()
	if len(msgs) == 0 || !contains(msgs[0], "no result tabs") {
		t.Errorf("expected 'no result tabs' toast; got %v", msgs)
	}
	chooser.mu.Lock()
	defer chooser.mu.Unlock()
	if chooser.calls != 0 {
		t.Errorf("chooser must not be invoked when no tab is active; calls=%d", chooser.calls)
	}
}

// TestSortPick_NoColumnsIsNoOp pins: SortPick on a tab without columns
// (no schema attached yet) does not call the chooser.
func TestSortPick_NoColumnsIsNoOp(t *testing.T) {
	chooser := &fakeChooser{}
	h, _ := newSortTestHelper(t, chooser)
	if err := h.openTab("Q", nil); err != nil {
		t.Fatalf("openTab: %v", err)
	}
	h.SortPick()
	chooser.mu.Lock()
	defer chooser.mu.Unlock()
	if chooser.calls != 0 {
		t.Errorf("chooser invoked despite empty columns; calls=%d", chooser.calls)
	}
}

// stubColumnStream is a minimal drivers.RowStream whose Columns() returns
// the configured slice. Used to verify the helper installs the streamed
// schema onto the tab's grid.View at attach time (dbsavvy-dqp).
type stubColumnStream struct {
	cols []models.ColumnMeta
}

func (s *stubColumnStream) Columns() []models.ColumnMeta { return s.cols }
func (s *stubColumnStream) Next(_ context.Context) (models.Row, bool, error) {
	return models.Row{}, false, nil
}
func (s *stubColumnStream) Close() error            { return nil }
func (s *stubColumnStream) QueryID() models.QueryID { return models.QueryID{} }
func (s *stubColumnStream) RowsAffected() int64     { return 0 }

// TestOpenTab_InstallsStreamColumnsOnGrid is the regression test for
// dbsavvy-dqp. Prior to the fix, the result grid stayed at zero columns
// for every streaming query because no path called grid.View.SetColumns
// with the stream's schema — so the grid rendered the "(0 rows)"
// EmptyResultIndicator regardless of how many rows were actually
// streamed in. After the fix, openTab installs the schema from
// RowStream.Columns() onto the tab's grid before the worker drains.
func TestOpenTab_InstallsStreamColumnsOnGrid(t *testing.T) {
	h, _ := newTestHelper(t, nil)
	cols := []models.ColumnMeta{
		{Name: "id", TypeName: "int8"},
		{Name: "email", TypeName: "text"},
		{Name: "name", TypeName: "text"},
	}
	rh := newFakeRunHandle()
	rh.rows = &stubColumnStream{cols: cols}

	if err := h.openTab("SELECT", rh); err != nil {
		t.Fatalf("openTab: %v", err)
	}
	tab := h.Active()
	if tab == nil {
		t.Fatal("no active tab")
	}
	got := tab.Grid().ColumnCount()
	if got != len(cols) {
		t.Fatalf("grid.ColumnCount() = %d, want %d (columns from RowStream.Columns() must be installed by openTab/startStreaming)", got, len(cols))
	}
	for i, want := range cols {
		if name := tab.Grid().ColumnName(i); name != want.Name {
			t.Errorf("grid.ColumnName(%d) = %q, want %q", i, name, want.Name)
		}
	}
}

// TestEditabilityIntrospectedAtStreamStart — editability is introspected
// as soon as the result columns are known (at stream start), NOT only at
// completion. Introspection is column-driven and runs on an isolated
// session, so the grid is marked editable while the tab is still
// StateRunning — letting inline edits work on buffered rows while a
// no-LIMIT query keeps streaming. dbsavvy-2b6, dbsavvy-1po.
func TestEditabilityIntrospectedAtStreamStart(t *testing.T) {
	runner := &fakeStreamRunner{}
	factory := func() StreamRunner { return runner }
	h, _ := newTestHelper(t, factory)

	h.deps.IntrospectEditability = func(_ context.Context, cols []models.ColumnMeta) (bool, []int, string, string) {
		return true, []int{0}, "", "myschema"
	}

	// The run handle must expose a RowStream with columns: introspection
	// keys off the streamed schema, installed before the first row.
	rh := newFakeRunHandle()
	rh.rows = &stubColumnStream{cols: []models.ColumnMeta{{Name: "id", TypeName: "int8"}}}

	_ = h.openTab("SELECT id FROM t", rh)
	tab := h.Active()

	// Editable BEFORE any onDone fires — i.e. while still StateRunning.
	if tab.State() != StateRunning {
		t.Fatalf("tab state = %q, want StateRunning (not yet complete)", tab.State())
	}
	if !tab.grid.Editable() {
		t.Fatal("grid not editable at stream start; want editable during StateRunning (dbsavvy-1po)")
	}
	ri := tab.grid.RowIdentity()
	if len(ri) != 1 || ri[0] != 0 {
		t.Fatalf("row identity = %v, want [0]", ri)
	}
	// The catalog-resolved schema must be threaded onto the grid so the
	// apply path can schema-qualify the UPDATE (dbsavvy-8q6).
	if got := tab.grid.IdentitySchema(); got != "myschema" {
		t.Fatalf("grid IdentitySchema = %q, want %q", got, "myschema")
	}

	// Completion must not regress editability.
	runner.fireOnDone()
	if !tab.grid.Editable() {
		t.Fatal("grid not editable after completion")
	}
}

// --- dbsavvy-usj: focus-stack IBaseContext for result tabs -----------------

// TestActiveContext_NilWhenNoTabs verifies that ActiveContext() returns
// nil before any tab is opened. The rail-switch handler relies on this
// to silently no-op digit 6 when no result tabs exist (dbsavvy-usj).
func TestActiveContext_NilWhenNoTabs(t *testing.T) {
	h, _ := newTestHelper(t, nil)
	if got := h.ActiveContext(); got != nil {
		t.Fatalf("ActiveContext() with no tabs = %v, want nil", got)
	}
}

// TestActiveContext_ResultTabReturnsResultGridKey verifies that the
// IBaseContext surfaced for a non-plan tab carries the RESULT_GRID Key
// (shared by all tabs so the cheatsheet + matcher resolve scope-keyed
// bindings) and a slot-specific ViewName, and is MAIN_CONTEXT (so
// ContextTree.Push lands it correctly on the main slot).
func TestActiveContext_ResultTabReturnsResultGridKey(t *testing.T) {
	h, _ := newTestHelper(t, nil)
	if err := h.openTab("SELECT 1", nil); err != nil {
		t.Fatalf("openTab: %v", err)
	}
	ctx := h.ActiveContext()
	if ctx == nil {
		t.Fatal("ActiveContext() with one open tab returned nil")
	}
	tab := h.Active()
	if tab == nil {
		t.Fatal("Active() with one open tab returned nil")
	}
	if ctx.GetKey() != types.RESULT_GRID {
		t.Errorf("ctx.GetKey() = %q, want %q", ctx.GetKey(), types.RESULT_GRID)
	}
	wantView := string(types.ResultTabKey(tab.Slot()))
	if ctx.GetViewName() != wantView {
		t.Errorf("ctx.GetViewName() = %q, want %q", ctx.GetViewName(), wantView)
	}
	if ctx.GetKind() != types.MAIN_CONTEXT {
		t.Errorf("ctx.GetKind() = %v, want MAIN_CONTEXT", ctx.GetKind())
	}
}

// TestActiveContext_PlanTabSurfacesPlanContext verifies that a plan tab
// surfaces its PlanContext (PLAN key) rather than the slot-specific
// BaseContext, so PLAN-scoped controller bindings dispatch correctly
// when focus lands on a plan tab. dbsavvy-usj.
func TestActiveContext_PlanTabSurfacesPlanContext(t *testing.T) {
	h, _ := newTestHelper(t, nil)
	if err := h.OpenPlanTab("EXPLAIN", models.Plan{RawText: "Seq Scan"}); err != nil {
		t.Fatalf("OpenPlanTab: %v", err)
	}
	ctx := h.ActiveContext()
	if ctx == nil {
		t.Fatal("ActiveContext() with one open plan tab returned nil")
	}
	if ctx.GetKey() != types.PLAN {
		t.Errorf("plan tab ctx.GetKey() = %q, want PLAN", ctx.GetKey())
	}
}

// --- LayoutPaint renders the data-tab footer --------------------------------

// TestLayoutPaintRendersDataTabTitle verifies a data tab's run metadata
// lands on view.Footer (the bottom-right footer) after LayoutPaint.
// Grid.Render owns view.Title (sort indicator only) and must not clobber
// the footer.
func TestLayoutPaintRendersDataTabTitle(t *testing.T) {
	runner := &fakeStreamRunner{}
	factory := func() StreamRunner { return runner }
	h, _ := newTestHelper(t, factory)

	if err := h.openTab("SELECT id FROM t", newFakeRunHandle()); err != nil {
		t.Fatalf("openTab: %v", err)
	}
	tab := h.Active()
	if tab == nil {
		t.Fatal("Active = nil after openTab")
	}
	g := tab.Grid()
	if g == nil {
		t.Fatal("data tab Grid() = nil; want non-nil so the data branch runs")
	}
	g.SetColumns([]models.ColumnMeta{{Name: "id", TypeName: "int"}})
	g.AppendRows([]models.Row{{Values: []any{1}}, {Values: []any{2}}, {Values: []any{3}}})

	// Mark the tab COMPLETE via the streaming onDone path (OnUIThread
	// nil → synchronous) so Title() reports the final row count.
	runner.fireOnDone()
	if !tab.Complete() {
		t.Fatal("tab not complete after fireOnDone")
	}

	rec := testfake.NewRecorderGuiDriver()
	name := tab.ViewName()
	rec.EnableRealView(name)

	h.LayoutPaint(rec, 0, 0, 80, 24)

	v := rec.RealView(name)
	if v == nil {
		t.Fatalf("RealView(%q) = nil; expected a real view after LayoutPaint", name)
	}
	if v.Footer == "" {
		t.Fatalf("data-tab view.Footer is empty after LayoutPaint; want %q", tab.Title())
	}
	if v.Footer != tab.Title() {
		t.Errorf("view.Footer = %q, want %q", v.Footer, tab.Title())
	}
}

// TestTitleRowsAffected covers the DML-without-RETURNING case: a completed
// tab with zero result rows but a non-zero command-tag count must report
// "N rows affected" rather than the misleading "0 rows". dbsavvy-tiu8.
func TestTitleRowsAffected(t *testing.T) {
	tests := []struct {
		name         string
		state        TabState
		complete     bool
		rowCount     int64
		rowsAffected int64
		want         string
	}{
		{"dml no returning", StateComplete, true, 0, 5, "5 rows affected"},
		{"dml single row", StateComplete, true, 0, 1, "1 rows affected"},
		{"select zero rows", StateComplete, true, 0, 0, "0 rows"},
		{"select with rows", StateComplete, true, 3, 3, "3 rows"},
		{"running not yet complete", StateRunning, false, 0, 0, "~0 rows · running"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tab := &Tab{
				state:        tc.state,
				complete:     tc.complete,
				rowCount:     tc.rowCount,
				rowsAffected: tc.rowsAffected,
			}
			if got := tab.Title(); got != tc.want {
				t.Errorf("Title() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestLayoutPaintRendersEmptyDataTabTitle covers the edge case: a
// completed data tab with zero rows must still set a non-empty footer
// (the grid still runs through Grid.Render with an empty result set).
func TestLayoutPaintRendersEmptyDataTabTitle(t *testing.T) {
	runner := &fakeStreamRunner{}
	factory := func() StreamRunner { return runner }
	h, _ := newTestHelper(t, factory)

	if err := h.openTab("SELECT id FROM t WHERE false", newFakeRunHandle()); err != nil {
		t.Fatalf("openTab: %v", err)
	}
	tab := h.Active()
	g := tab.Grid()
	if g == nil {
		t.Fatal("data tab Grid() = nil; want non-nil")
	}
	g.SetColumns([]models.ColumnMeta{{Name: "id", TypeName: "int"}})
	// No rows appended.
	runner.fireOnDone()

	rec := testfake.NewRecorderGuiDriver()
	name := tab.ViewName()
	rec.EnableRealView(name)
	h.LayoutPaint(rec, 0, 0, 80, 24)

	v := rec.RealView(name)
	if v == nil {
		t.Fatalf("RealView(%q) = nil after LayoutPaint", name)
	}
	if v.Footer == "" {
		t.Fatalf("empty (0-row) completed data-tab view.Footer is empty; want %q", tab.Title())
	}
	if v.Footer != tab.Title() {
		t.Errorf("view.Footer = %q, want %q", v.Footer, tab.Title())
	}
}

// TestLayoutPaintRendersPlanAndErrorTabTitles is the non-regression guard
// for the non-grid branches: plan and error tabs (which skip Grid.Render)
// must still set their footer metadata.
func TestLayoutPaintRendersPlanAndErrorTabTitles(t *testing.T) {
	h, _ := newTestHelper(t, nil)

	if err := h.OpenPlanTab("EXPLAIN SELECT", models.Plan{RawText: "Seq Scan on users"}); err != nil {
		t.Fatalf("OpenPlanTab: %v", err)
	}
	planTab := h.Active()
	if planTab.State() != StatePlan {
		t.Fatalf("plan tab State = %v, want Plan", planTab.State())
	}

	h.ShowError("SELECT bad", errors.New("syntax error near 'WHERE'"))
	errTab := h.Active()
	if errTab.State() != StateError {
		t.Fatalf("error tab State = %v, want Error", errTab.State())
	}

	rec := testfake.NewRecorderGuiDriver()
	rec.EnableRealView(planTab.ViewName())
	rec.EnableRealView(errTab.ViewName())

	h.LayoutPaint(rec, 0, 0, 80, 24)

	pv := rec.RealView(planTab.ViewName())
	if pv == nil || pv.Footer == "" {
		t.Fatalf("plan-tab view/footer empty after LayoutPaint: view=%v", pv)
	}
	ev := rec.RealView(errTab.ViewName())
	if ev == nil || ev.Footer == "" {
		t.Fatalf("error-tab view/footer empty after LayoutPaint: view=%v", ev)
	}
}
