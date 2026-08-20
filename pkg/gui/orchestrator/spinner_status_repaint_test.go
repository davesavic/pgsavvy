package orchestrator_test

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/pgsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/pgsavvy/pkg/gui/status"
)

// statusRepaintDriver wraps the recorder with atomic counters for the
// observations the tick-repaint tests make while the drain goroutine is
// concurrently invoking the driver: SetContent writes to the status view,
// full Update calls, and UpdateContentOnly calls. Raw RecorderGuiDriver
// field reads would race those writes under -race.
type statusRepaintDriver struct {
	*testfake.RecorderGuiDriver
	statusWrites atomic.Int64
	updates      atomic.Int64
	contentOnly  atomic.Int64
}

func (d *statusRepaintDriver) SetContent(viewName, str string) error {
	if viewName == orchestrator.AppStatusViewName {
		d.statusWrites.Add(1)
	}
	return d.RecorderGuiDriver.SetContent(viewName, str)
}

func (d *statusRepaintDriver) Update(fn func() error) {
	d.updates.Add(1)
	d.RecorderGuiDriver.Update(fn)
}

func (d *statusRepaintDriver) UpdateContentOnly(fn func() error) {
	d.contentOnly.Add(1)
	d.RecorderGuiDriver.UpdateContentOnly(fn)
}

// busyWorkerForTest arms one blocking OnWorker (busy==1) and installs a
// cleanup that releases it and joins the pool, so a mid-test Fatal cannot
// leave a blocked worker hanging the deferred Close.
func busyWorkerForTest(t *testing.T, g busyCounter) (arm func(), waitStarted func()) {
	t.Helper()
	release := make(chan struct{})
	started := make(chan struct{})
	arm = func() {
		g.OnWorker(func(_ gocui.Task) error {
			close(started)
			<-release
			return nil
		})
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		g.WaitWorkers()
		_ = g.Close()
	})
	return arm, func() { <-started }
}

// busyCounter is the surface busyWorkerForTest needs from the Gui.
type busyCounter interface {
	OnWorker(func(gocui.Task) error)
	WaitWorkers()
	Close() error
}

// spinnerGlyphIn returns the braille spinner glyph present in the status
// buffer, or ok=false when none is (quiescent line carries no glyph).
func spinnerGlyphIn(buf string) (rune, bool) {
	for frame := range int64(10) {
		glyph := status.SpinnerGlyph(frame)
		if strings.ContainsRune(buf, glyph) {
			return glyph, true
		}
	}
	return 0, false
}

// waitForStatusBuffer polls the status view buffer until cond holds or a
// 1s deadline elapses (the drain goroutine lands its repaint shortly
// after tickAll; polling mirrors waitModalWrites).
func waitForStatusBuffer(t *testing.T, drv *statusRepaintDriver, what string, cond func(string) bool) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		buf := drv.GetViewBuffer(orchestrator.AppStatusViewName)
		if cond(buf) {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("status buffer never satisfied %q; last=%q", what, buf)
		case <-time.After(time.Millisecond):
		}
	}
}

// settleCounters waits d for the driver counters to stop moving, then
// returns the settled (updates, contentOnly) snapshot. The forced-layout
// tick legitimately cascades extra driver.Update calls (the full layout
// re-renders the CONNECTION_MANAGER modal, whose HandleRender writes via
// Update) — snapshots must be taken only once that cascade has quiesced.
func settleCounters(drv *statusRepaintDriver, d time.Duration) (int64, int64) {
	deadline := time.After(d)
	for {
		u1, c1 := drv.updates.Load(), drv.contentOnly.Load()
		time.Sleep(20 * time.Millisecond)
		u2, c2 := drv.updates.Load(), drv.contentOnly.Load()
		if u1 == u2 && c1 == c2 {
			return u2, c2
		}
		select {
		case <-deadline:
			return u2, c2
		default:
		}
	}
}

// TestSpinnerTick_RepaintsStatusLine_AdvancingGlyph pins AC1: with
// BusyCount==1 and NO keypress, the spinner tick repaints the status view
// and the glyph advances — ≥2 distinct frames over ≥3 ticks, observed via
// EnableRealView + GetViewBuffer on the recorder.
func TestSpinnerTick_RepaintsStatusLine_AdvancingGlyph(t *testing.T) {
	clk := newFakeClock()
	drv := &statusRepaintDriver{RecorderGuiDriver: testfake.NewRecorderGuiDriver()}
	drv.EnableRealView(orchestrator.AppStatusViewName)
	g := buildTestGuiWithDriverAndClock(t, drv, clk)

	// Materialize the status view + initial (quiescent, glyph-less)
	// paint through the full layout path.
	if err := drv.SetSize(80, 24); err != nil {
		t.Fatalf("SetSize: %v", err)
	}
	waitForStatusBuffer(t, drv, "initial quiescent paint", func(b string) bool {
		return b != ""
	})
	if _, has := spinnerGlyphIn(drv.GetViewBuffer(orchestrator.AppStatusViewName)); has {
		t.Fatal("pre-condition: quiescent status line already carries a spinner glyph")
	}

	arm, waitStarted := busyWorkerForTest(t, g)
	arm()
	waitStarted()
	if got := g.BusyCount(); got != 1 {
		t.Fatalf("BusyCount=%d, want 1", got)
	}

	// ≥3 ticks, each advancing simulated wall-clock by one
	// spinnerTickInterval so every repaint selects the NEXT glyph.
	seen := make(map[rune]bool)
	prev := drv.GetViewBuffer(orchestrator.AppStatusViewName)
	const ticks = 4
	for i := range ticks {
		clk.Advance(100 * time.Millisecond)
		clk.tickAll()
		waitForStatusBuffer(t, drv, "advanced glyph", func(b string) bool {
			return b != prev
		})
		buf := drv.GetViewBuffer(orchestrator.AppStatusViewName)
		glyph, ok := spinnerGlyphIn(buf)
		if !ok {
			t.Fatalf("tick %d: status buffer carries no spinner glyph while busy==1: %q", i+1, buf)
		}
		seen[glyph] = true
		prev = buf
	}
	if len(seen) < 2 {
		t.Fatalf("spinner did not advance: %d distinct glyphs over %d ticks, want >= 2", len(seen), ticks)
	}
}

// TestSpinnerTick_ResizeForcesFullLayoutBeforeContentOnlyTicks pins AC5:
// a terminal resize observed by the layout pass mid-busy makes the NEXT
// spinner tick force exactly ONE full layout (OnUIThread) — refreshing the
// status rect — before content-only ticks resume; the rect from the Tier-4a
// SetView pass is never stale in tick output.
func TestSpinnerTick_ResizeForcesFullLayoutBeforeContentOnlyTicks(t *testing.T) {
	clk := newFakeClock()
	drv := &statusRepaintDriver{RecorderGuiDriver: testfake.NewRecorderGuiDriver()}
	g := buildTestGuiWithDriverAndClock(t, drv, clk)

	if err := drv.SetSize(80, 24); err != nil {
		t.Fatalf("SetSize: %v", err)
	}

	arm, waitStarted := busyWorkerForTest(t, g)
	arm()
	waitStarted()

	// Baseline: one content-only tick repaints the status line only.
	clk.Advance(100 * time.Millisecond)
	clk.tickAll()
	deadline := time.After(time.Second)
	for drv.contentOnly.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("first tick did not route through OnUIThreadContentOnly")
		case <-time.After(time.Millisecond):
		}
	}
	// Terminal resize mid-busy: the driver's resize path runs the full
	// layout at the new geometry, which flags the tick path. (The resize's
	// own RunLayout may legitimately cascade modal Update writes.)
	if err := drv.SetSize(120, 40); err != nil {
		t.Fatalf("SetSize(resize): %v", err)
	}
	updatesAfterResize, _ := settleCounters(drv, 200*time.Millisecond)

	// The first tick after the resize must force exactly ONE full layout
	// pass (OnUIThread) instead of its content-only repaint.
	clk.Advance(100 * time.Millisecond)
	clk.tickAll()
	deadline = time.After(time.Second)
	for drv.updates.Load() <= updatesAfterResize {
		select {
		case <-deadline:
			t.Fatalf("tick after resize did not force a full layout; updates=%d want > %d", drv.updates.Load(), updatesAfterResize)
		case <-time.After(time.Millisecond):
		}
	}
	updatesAfterForce, _ := settleCounters(drv, 200*time.Millisecond)

	// The forced layout re-ran the Tier-4a SetView at the NEW geometry —
	// the status rect a subsequent tick paints into is fresh.
	want := ui.GetWindowDimensions(120, 40)[orchestrator.AppStatusViewName]
	var lastStatus testfake.SetViewCall
	found := false
	for _, c := range drv.AllSetViewCalls() {
		if c.Name == orchestrator.AppStatusViewName {
			lastStatus, found = c, true
		}
	}
	if !found {
		t.Fatal("no SetView recorded for the status view")
	}
	if lastStatus.X0 != want.X0 || lastStatus.Y0 != want.Y0-1 ||
		lastStatus.X1 != want.X1 || lastStatus.Y1 != want.Y1+1 {
		t.Fatalf("status rect after resize+forced layout = %+v, want (%d,%d,%d,%d) — stale rect in tick output",
			lastStatus, want.X0, want.Y0-1, want.X1, want.Y1+1)
	}

	// Content-only ticks resume: two further ticks grow ContentOnlyCalls
	// and paint new content, but add NO further full layouts.
	prev := drv.GetViewBuffer(orchestrator.AppStatusViewName)
	for range 2 {
		clk.Advance(100 * time.Millisecond)
		clk.tickAll()
		p := prev
		waitForStatusBuffer(t, drv, "resumed content-only tick", func(b string) bool {
			return b != p
		})
		prev = drv.GetViewBuffer(orchestrator.AppStatusViewName)
	}
	updatesSettled, contentOnlySettled := settleCounters(drv, 200*time.Millisecond)
	if updatesSettled != updatesAfterForce {
		t.Fatalf("UpdateCalls=%d after resumed ticks, want unchanged %d — resize must force exactly one full layout", updatesSettled, updatesAfterForce)
	}
	if contentOnlySettled < 3 {
		t.Fatalf("ContentOnlyCalls=%d, want >= 3 (baseline + 2 resumed ticks)", contentOnlySettled)
	}
}

// TestSpinnerTickToast_TTLDeterministicUnderBusyRepaint pins AC4: toast
// expiry is wall-clock under the 10Hz busy repaint — the toast survives
// every repaint before its TTL (no instant vanish) and is gone by the
// first repaint after it (never outlives TTL).
func TestSpinnerTickToast_TTLDeterministicUnderBusyRepaint(t *testing.T) {
	clk := newFakeClock()
	drv := &statusRepaintDriver{RecorderGuiDriver: testfake.NewRecorderGuiDriver()}
	g := buildTestGuiWithDriverAndClock(t, drv, clk)

	if err := drv.SetSize(80, 24); err != nil {
		t.Fatalf("SetSize: %v", err)
	}

	arm, waitStarted := busyWorkerForTest(t, g)
	arm()
	waitStarted()

	const msg = "saving world"
	const ttl = 400 * time.Millisecond
	g.ToastHelpForTest().Show(msg, ttl)

	// No instant vanish: two full repaint cycles land INSIDE the TTL
	// window (fake-clock advances cost ~0 real time) and each still
	// carries the toast — the multiplex reads Current() live per repaint.
	for range 2 {
		clk.Advance(100 * time.Millisecond)
		clk.tickAll()
		waitForStatusBuffer(t, drv, "toast visible under busy repaint", func(b string) bool {
			return strings.Contains(b, msg)
		})
	}

	// Expiry: let the real-time TTL elapse, then repaint — the toast must
	// be gone (auto-clear ran via the driver) and stay gone.
	time.Sleep(ttl + 100*time.Millisecond)
	clk.Advance(100 * time.Millisecond)
	clk.tickAll()
	waitForStatusBuffer(t, drv, "toast expired", func(b string) bool {
		return !strings.Contains(b, msg)
	})
	for range 2 {
		clk.Advance(100 * time.Millisecond)
		clk.tickAll()
	}
	deadline := time.After(150 * time.Millisecond)
	for {
		if buf := drv.GetViewBuffer(orchestrator.AppStatusViewName); strings.Contains(buf, msg) {
			t.Fatalf("toast outlived TTL: buffer still carries %q", msg)
		}
		select {
		case <-deadline:
		case <-time.After(time.Millisecond):
			continue
		}
		break
	}
}

// TestSpinnerTick_SuppressedWhilePromptOnTop_NoStatusRepaint pins the
// generalized suppression half of AC6: while a prompt popup owns the top
// of the focus stack, a spinner tick repaints NEITHER the connecting modal
// (existing keep-green) NOR the status line; after the prompt leaves the
// top, the next tick resumes the status repaint. The routing half (tick
// repaint via OnUIThreadContentOnly only, never the drain goroutine) is
// asserted by the Update/UpdateContentOnly counters in the resize test
// above and by TestSpinnerFrame_AdvancesOverSimulatedTime.
func TestSpinnerTick_SuppressedWhilePromptOnTop_NoStatusRepaint(t *testing.T) {
	clk := newFakeClock()
	drv := &statusRepaintDriver{RecorderGuiDriver: testfake.NewRecorderGuiDriver()}
	g := buildTestGuiWithDriverAndClock(t, drv, clk)

	if err := drv.SetSize(80, 24); err != nil {
		t.Fatalf("SetSize: %v", err)
	}

	// Prompt popup on top of the focus stack, exactly as the masked
	// credential prompt sits mid-connect.
	if err := g.ContextTree().Push(g.Registry().Prompt); err != nil {
		t.Fatalf("push prompt: %v", err)
	}

	arm, waitStarted := busyWorkerForTest(t, g)
	arm()
	waitStarted()

	writesBefore := drv.statusWrites.Load()
	const ticks = 3
	for range ticks {
		clk.Advance(100 * time.Millisecond)
		clk.tickAll()
	}
	// Expect ZERO status writes while the prompt owns the top of the
	// stack; the settle window fails fast if any lands.
	settleNoStatusWrites(drv, 50*time.Millisecond)
	if got := drv.statusWrites.Load() - writesBefore; got != 0 {
		t.Fatalf("prompt-on-top: status writes=%d after %d ticks, want 0", got, ticks)
	}

	// Pop the prompt: the next tick must resume the status repaint.
	if err := g.ContextTree().Pop(); err != nil {
		t.Fatalf("pop prompt: %v", err)
	}
	clk.Advance(100 * time.Millisecond)
	clk.tickAll()
	deadline := time.After(time.Second)
	for drv.statusWrites.Load() <= writesBefore {
		select {
		case <-deadline:
			t.Fatalf("status repaint did not resume after prompt popped (writes=%d)", drv.statusWrites.Load())
		case <-time.After(time.Millisecond):
		}
	}
}

// settleNoStatusWrites lets the drain goroutine run for d, returning
// early the moment a status write appears (regressions fail fast).
func settleNoStatusWrites(drv *statusRepaintDriver, d time.Duration) {
	deadline := time.After(d)
	for {
		if drv.statusWrites.Load() > 0 {
			return
		}
		select {
		case <-deadline:
			return
		case <-time.After(time.Millisecond):
		}
	}
}
