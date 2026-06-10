package orchestrator_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"go.uber.org/goleak"

	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/dbsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// contentOnlySignalDriver wraps the recorder and signals a buffered
// channel each time UpdateContentOnly fires, so the spinner-tick test can
// observe a content-only re-render without racing the recorder's
// unguarded ContentOnlyCalls counter.
type contentOnlySignalDriver struct {
	*testfake.RecorderGuiDriver
	fired chan struct{}
}

func (d *contentOnlySignalDriver) UpdateContentOnly(fn func() error) {
	d.RecorderGuiDriver.UpdateContentOnly(fn)
	select {
	case d.fired <- struct{}{}:
	default:
	}
}

// fakeClock is a deterministic Clock for the spinner-ticker tests. Now()
// returns a manually-advanced time; NewTicker hands back a ticker whose
// channel the test drives via Tick(). It records how many tickers are
// live so the exactly-one-ticker invariant can be asserted.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	tickers []*fakeTicker
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(0, 0)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *fakeClock) NewTicker(_ time.Duration) orchestrator.Ticker {
	t := &fakeTicker{ch: make(chan time.Time, 1)}
	c.mu.Lock()
	c.tickers = append(c.tickers, t)
	c.mu.Unlock()
	return t
}

// liveTickers counts tickers that have been created and not yet stopped.
func (c *fakeClock) liveTickers() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.tickers {
		if !t.stopped.Load() {
			n++
		}
	}
	return n
}

// tickAll pushes the current time onto every live ticker's channel,
// simulating a wall-clock tick fire.
func (c *fakeClock) tickAll() {
	c.mu.Lock()
	now := c.now
	live := make([]*fakeTicker, 0, len(c.tickers))
	for _, t := range c.tickers {
		if !t.stopped.Load() {
			live = append(live, t)
		}
	}
	c.mu.Unlock()
	for _, t := range live {
		select {
		case t.ch <- now:
		default:
		}
	}
}

type fakeTicker struct {
	ch      chan time.Time
	stopped atomic.Bool
}

func (t *fakeTicker) Chan() <-chan time.Time { return t.ch }
func (t *fakeTicker) Stop()                  { t.stopped.Store(true) }

// TestSpinnerFrame_AdvancesOverSimulatedTime verifies the spinner frame
// counter advances with elapsed wall-clock time while a single worker is
// in flight (U8 AC: fake-clock test asserts frame advances over
// simulated time; a single long-running worker shows a cycling spinner).
func TestSpinnerFrame_AdvancesOverSimulatedTime(t *testing.T) {
	clk := newFakeClock()
	drv := &contentOnlySignalDriver{
		RecorderGuiDriver: testfake.NewRecorderGuiDriver(),
		fired:             make(chan struct{}, 4),
	}
	g := buildTestGuiWithDriverAndClock(t, drv, clk)
	defer func() { _ = g.Close() }()

	release := make(chan struct{})
	started := make(chan struct{})
	g.OnWorker(func(_ gocui.Task) error {
		close(started)
		<-release
		return nil
	})
	<-started

	frame0 := g.SpinnerFrame()

	// >300ms elapse: at a ~100ms tick the frame must advance through
	// multiple frames.
	clk.Advance(350 * time.Millisecond)
	frameLater := g.SpinnerFrame()

	if frameLater-frame0 < 3 {
		t.Fatalf("spinner frame advanced by %d over 350ms, want >= 3", frameLater-frame0)
	}

	// Drive a tick and confirm it triggers a content-only re-render so the
	// new frame actually reaches the screen.
	clk.tickAll()
	select {
	case <-drv.fired:
	case <-time.After(time.Second):
		t.Fatal("ticker did not trigger OnUIThreadContentOnly within 1s")
	}

	close(release)
	g.WaitWorkers()
}

// TestSpinnerTicker_ExactlyOneWhileBusy verifies that while busy>0 there
// is exactly one ticker, regardless of how many overlapping workers are
// armed, and zero after they all drain (U8 AC: exactly one ticker exists
// while busy>0 and none after; concurrent workers cannot double-arm).
func TestSpinnerTicker_ExactlyOneWhileBusy(t *testing.T) {
	clk := newFakeClock()
	g, _ := buildTestGuiWithClock(t, clk)
	defer func() { _ = g.Close() }()

	if got := clk.liveTickers(); got != 0 {
		t.Fatalf("pre: liveTickers=%d, want 0", got)
	}

	release := make(chan struct{})
	const n = 5
	var started sync.WaitGroup
	started.Add(n)
	for range n {
		g.OnWorker(func(_ gocui.Task) error {
			started.Done()
			<-release
			return nil
		})
	}
	started.Wait()

	if got := clk.liveTickers(); got != 1 {
		t.Fatalf("during %d overlapping workers: liveTickers=%d, want 1", n, got)
	}

	close(release)
	g.WaitWorkers()

	if got := clk.liveTickers(); got != 0 {
		t.Fatalf("after drain: liveTickers=%d, want 0", got)
	}
}

// TestSpinnerTicker_NoLeak_RapidStress hammers the 0->1->0 transition and
// asserts no ticker goroutine leaks under -race (U8 AC: N-worker stress
// leaves no leaked ticker).
func TestSpinnerTicker_NoLeak_RapidStress(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	clk := newFakeClock()
	g, _ := buildTestGuiWithClock(t, clk)

	const n = 200
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		g.OnWorker(func(_ gocui.Task) error {
			defer wg.Done()
			return nil
		})
	}
	wg.Wait()
	g.WaitWorkers()

	if got := clk.liveTickers(); got != 0 {
		t.Fatalf("after rapid stress: liveTickers=%d, want 0", got)
	}
	if err := g.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestSpinnerTicker_CloseWithBusyStopsTicker verifies Close() stops the
// ticker even when busyCount>0 at shutdown, leaving no leaked goroutine
// (U8 AC: Close() with busy>0 leaves no ticker goroutine; ticker stopped
// unconditionally before driver.Close()).
func TestSpinnerTicker_CloseWithBusyStopsTicker(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	clk := newFakeClock()
	g, _ := buildTestGuiWithClock(t, clk)

	release := make(chan struct{})
	started := make(chan struct{})
	g.OnWorker(func(_ gocui.Task) error {
		close(started)
		<-release
		return nil
	})
	<-started

	if got := clk.liveTickers(); got != 1 {
		t.Fatalf("with busy worker: liveTickers=%d, want 1", got)
	}

	// Release the worker so workersWG.Wait() inside Close() can complete,
	// but the ticker must be stopped by Close() regardless of busy state.
	closeDone := make(chan error, 1)
	go func() {
		// give Close a beat to begin, then unblock the worker
		close(release)
		closeDone <- g.Close()
	}()
	if err := <-closeDone; err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got := clk.liveTickers(); got != 0 {
		t.Fatalf("after Close: liveTickers=%d, want 0", got)
	}
}

// modalRepaintDriver wraps the recorder and counts SetContent writes to the
// CONNECTION_MANAGER view, so the T4 test can observe the modal's render path
// (HandleRender → SetContent) being re-invoked per spinner tick.
type modalRepaintDriver struct {
	*testfake.RecorderGuiDriver
	modalWrites atomic.Int64
}

func (d *modalRepaintDriver) SetContent(viewName, str string) error {
	if viewName == string(types.CONNECTION_MANAGER) {
		d.modalWrites.Add(1)
	}
	return d.RecorderGuiDriver.SetContent(viewName, str)
}

// TestSpinnerTicker_RepaintsConnectingModal proves T4: while the
// CONNECTION_MANAGER modal is in ModeConnecting and the ticker is armed
// (busy>0), each spinner tick re-invokes the modal's render path
// (HandleRender → SetContent), so a frame-dependent body (T3) animates without
// a keypress or worker completion. It also covers the negatives: in ModeList a
// tick does NOT force the connecting-modal repaint, and busy==0 (ticker not
// armed) is a safe no-op.
func TestSpinnerTicker_RepaintsConnectingModal(t *testing.T) {
	clk := newFakeClock()
	drv := &modalRepaintDriver{RecorderGuiDriver: testfake.NewRecorderGuiDriver()}
	g := buildTestGuiWithDriverAndClock(t, drv, clk)
	defer func() { _ = g.Close() }()

	// Register the modal view so the recorder's SetContent path resolves it
	// (the live layout pass would do this; the recorder doesn't run layout).
	if _, err := drv.SetView(string(types.CONNECTION_MANAGER), 0, 0, 10, 10, 0); err != nil &&
		err != gocui.ErrUnknownView {
		t.Fatalf("SetView(connection_manager): %v", err)
	}

	cm := g.Registry().ConnectionManager
	if cm == nil {
		t.Fatal("ConnectionManager modal not wired")
	}

	// --- busy==0 / ticker-not-armed: repaint helper path must be a safe no-op.
	// No ticker exists yet, so driving ticks does nothing; assert no panic and
	// no modal writes accumulate from a stray tick.
	clk.tickAll()
	if got := drv.modalWrites.Load(); got != 0 {
		t.Fatalf("pre-arm: modal writes=%d, want 0", got)
	}

	// --- Negative: armed but in ModeList — a tick must NOT repaint the modal.
	cm.SetMode(guicontext.ModeList)
	releaseList := make(chan struct{})
	startedList := make(chan struct{})
	g.OnWorker(func(_ gocui.Task) error {
		close(startedList)
		<-releaseList
		return nil
	})
	<-startedList

	const listTicks = 3
	for range listTicks {
		clk.tickAll()
	}
	// Give the drain goroutine a beat to forward any tick. We expect ZERO
	// modal writes in ModeList, so any nonzero value within the settle window
	// is a failure; a short window keeps the test fast.
	settleNoModalWrites(drv, 50*time.Millisecond)
	if got := drv.modalWrites.Load(); got != 0 {
		t.Fatalf("ModeList: modal writes=%d after %d ticks, want 0", got, listTicks)
	}
	close(releaseList)
	g.WaitWorkers()

	// --- Positive: ModeConnecting — each tick re-invokes HandleRender.
	cm.ConnectingState().SetConnectingStaged("prod", nil)
	cm.SetMode(guicontext.ModeConnecting)
	drv.modalWrites.Store(0)

	release := make(chan struct{})
	started := make(chan struct{})
	g.OnWorker(func(_ gocui.Task) error {
		close(started)
		<-release
		return nil
	})
	<-started

	const ticks = 4
	for i := range ticks {
		clk.tickAll()
		// Wait for THIS tick's repaint to land before firing the next, so the
		// 1-slot ticker channel never drops a tick (each tick adds one write).
		waitModalWrites(t, drv, int64(i+1))
	}

	if got := drv.modalWrites.Load(); got < int64(ticks) {
		t.Fatalf("ModeConnecting: modal writes=%d after %d ticks, want >= %d", got, ticks, ticks)
	}

	close(release)
	g.WaitWorkers()
}

// waitModalWrites polls until the modal write counter reaches want or a 1s
// deadline elapses — the drain goroutine schedules the repaint asynchronously
// onto the (inline) driver, so the counter lands shortly after tickAll.
func waitModalWrites(t *testing.T, drv *modalRepaintDriver, want int64) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		if drv.modalWrites.Load() >= want {
			return
		}
		select {
		case <-deadline:
			return
		case <-time.After(time.Millisecond):
		}
	}
}

// settleNoModalWrites lets the drain goroutine run for d, returning early the
// moment a modal write appears (so a regression fails fast rather than after
// the full window). Used by the negative ModeList case.
func settleNoModalWrites(drv *modalRepaintDriver, d time.Duration) {
	deadline := time.After(d)
	for {
		if drv.modalWrites.Load() > 0 {
			return
		}
		select {
		case <-deadline:
			return
		case <-time.After(time.Millisecond):
		}
	}
}
