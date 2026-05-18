package ui

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/drivers"
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
	mu       sync.Mutex
	starts   int
	stops    int
	lastKey  string
	lastInit int
}

func (f *fakeStreamRunner) NewQueryTask(
	taskKey string,
	_ func(ctx context.Context) (drivers.RowStream, error),
	_ func([]models.Row),
	initialRows int,
	_ func(),
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	f.lastKey = taskKey
	f.lastInit = initialRows
	return nil
}

func (f *fakeStreamRunner) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
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

// --- Title ----------------------------------------------------------------

func TestTabTitleFormat(t *testing.T) {
	h, _ := newTestHelper(t, nil)
	_ = h.openTab("SELECT * FROM users", nil)
	active := h.Active()
	want := "result 1: SELECT * FROM users (running, 0 rows)"
	if got := active.Title(); got != want {
		t.Errorf("Title = %q, want %q", got, want)
	}
}

func TestTabTitleTruncatesLongLabel(t *testing.T) {
	h, _ := newTestHelper(t, nil)
	long := "SELECT a, b, c, d, e, f, g, h, i, j, k FROM very_long_table_name WHERE x = 1"
	_ = h.openTab(long, nil)
	title := h.Active().Title()
	if !contains(title, "…") {
		t.Errorf("Title = %q, expected ellipsis suffix on long label", title)
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
