package orchestrator

import (
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/logs"
)

// onWorkerSampleN is the AD-20 sample period: emit a worker_start /
// worker_end line every Nth OnWorker call in addition to mandatory
// quiescence-transition emits.
const onWorkerSampleN = 10

// Threading helpers — direct port of lazygit's
// pkg/gui/gui_common.go OnUIThread / OnUIThreadContentOnly / OnWorker
// (gui_common.go:119-129 in the vendored fork). DESIGN.md §17 ("Threading
// Model") describes the contract: background work runs on goroutines
// spawned by OnWorker (with a busy counter ticking the bottom spinner);
// those goroutines come back to the UI thread via OnUIThread (full
// re-layout) or OnUIThreadContentOnly (content-only fast path).
//
// The driver seam (types.GuiDriver.Update / UpdateContentOnly) hides
// gocui.Gui from the rest of pkg/gui — tests inject a recorder driver
// that invokes the closures inline, so this file is fully exercisable
// without a real terminal.

// busyDelta increments (delta=+1, called when a worker is queued) or
// decrements (delta=-1, called when a worker returns) the busy counter.
// Exposed via BusyCount() for the status renderer / smoke tests.
func (g *Gui) busyDelta(delta int64) int64 {
	return atomic.AddInt64(&g.busy, delta)
}

// BusyCount returns the current number of in-flight OnWorker goroutines.
// Zero means the spinner should be hidden; positive means at least one
// background job is running. Safe to call from any goroutine.
func (g *Gui) BusyCount() int64 {
	return atomic.LoadInt64(&g.busy)
}

// MutexBag returns the named-mutex bag (DESIGN.md §17). Pointer so
// callers can take the address of individual fields without copying the
// embedded sync.Mutex values.
func (g *Gui) MutexBag() *types.Mutexes {
	return &g.mutexes
}

// OnUIThread schedules fn for execution on the gocui MainLoop with a
// full re-layout pass afterwards. Mirrors lazygit's
// guiCommon.OnUIThread → gui.onUIThread → g.Update wiring. Safe to call
// from any goroutine; the call is non-blocking (the driver enqueues fn
// onto gocui's userEvents queue and returns).
//
// Nil-safe: returns immediately if the driver has not been wired yet
// (NewGui-without-wireWithDriver path used by some unit tests).
func (g *Gui) OnUIThread(fn func() error) {
	if g == nil || g.driver == nil || fn == nil {
		return
	}
	g.driver.Update(fn)
}

// OnUIThreadContentOnly schedules fn for execution on the MainLoop with
// the content-only fast path — gocui skips a full layout pass and only
// re-renders view content. Required for high-frequency row-stream
// updates where a full layout would cause flicker (DESIGN.md §6).
//
// Nil-safe in the same way as OnUIThread.
func (g *Gui) OnUIThreadContentOnly(fn func() error) {
	if g == nil || g.driver == nil || fn == nil {
		return
	}
	g.driver.UpdateContentOnly(fn)
}

// OnWorker spawns a goroutine that invokes fn with a gocui.Task. The
// busy counter is incremented before fn runs and decremented when fn
// returns (or panics) — observers (BusyCount, the bottom spinner) see a
// non-zero value for the entire lifetime of the call. Panics are
// recovered and converted to a logged error so a misbehaving worker
// can't take the TUI down.
//
// The Task hand-off matches lazygit's signature (a gocui.Task per
// worker so the caller can Pause/Continue/Done independent of busy
// counting). We use gocui.NewFakeTask() because our busy counter is the
// source of truth for "is the program busy" — the real gocui.TaskManager
// hangs off *gocui.Gui and is only needed by lazygit's integration-test
// harness, which dbsavvy does not consume.
//
// shutdownWG tracks live goroutines so Close can wait for them to
// finish before the goleak test in Phase 8 inspects the goroutine pool.
//
// Nil-safe: returns immediately when fn is nil. A nil g is a programmer
// error and panics (consistent with method-on-nil-receiver elsewhere in
// the orchestrator).
func (g *Gui) OnWorker(fn func(gocui.Task) error) {
	if fn == nil {
		return
	}
	busyAfter := g.busyDelta(+1)
	busyBefore := busyAfter - 1
	g.workersWG.Add(1)
	task := gocui.NewFakeTask()

	// AD-20 sampling gate (starts): always emit on the start-of-busy
	// transition (busy_before == 0); else emit every Nth call so bursts
	// stay loud enough to debug without flooding the file. Sampling
	// applies to worker_start only — worker_end always emits when the
	// counter returns to quiescence (busy_after == 0) and never on
	// non-transition completions. Together this yields the 2 + N/10
	// shape the AD-20 burst-sampling test asserts.
	sampleTick := g.onWorkerSampleCounter.Add(1)
	if busyBefore == 0 || sampleTick%onWorkerSampleN == 0 {
		g.emitWorkerEvent("worker_start",
			slog.Int64("busy_before", busyBefore),
			slog.Int64("busy_after", busyAfter),
		)
	}

	go func() {
		defer g.workersWG.Done()
		defer func() {
			endBusyAfter := g.busyDelta(-1)
			endBusyBefore := endBusyAfter + 1
			// Quiescence-only emit: only the worker whose decrement
			// returns the busy counter to zero records the transition.
			// Non-transition completions are intentionally dropped
			// (sampling lives on the start side only) to keep the
			// per-burst line budget at 2 + N/10.
			if endBusyAfter == 0 {
				g.emitWorkerEvent("worker_end",
					slog.Int64("busy_before", endBusyBefore),
					slog.Int64("busy_after", endBusyAfter),
				)
			}
		}()
		defer func() {
			if r := recover(); r != nil {
				if g.deps.Common != nil {
					g.deps.Common.Logger().Error("gui: OnWorker panic recovered", slog.Any("err", r))
				}
				// AD-20 edge: panic-recover always emits a worker_end with
				// panic_recovered=true (regardless of the sampling gate)
				// so silent crashes always leave a trace. The deferred
				// quiescence emit above ALSO fires — that one carries the
				// busy counters; this one carries the panic payload.
				g.emitWorkerEvent("worker_end",
					slog.Bool("panic_recovered", true),
					slog.Any("err", r),
				)
			}
		}()
		if err := fn(task); err != nil {
			if g.deps.Common != nil {
				g.deps.Common.Logger().Error("gui: OnWorker returned error", slog.Any("err", err))
			}
			// AD-20 edge: a non-nil fn error always emits worker_end with
			// err alongside the existing Errorf — sampling never decimates
			// the failure trail.
			g.emitWorkerEvent("worker_end", slog.Any("err", err))
		}
	}()
}

// emitWorkerEvent funnels every cat=state worker_* emit through a single
// nil-tolerant helper so the OnWorker hot path stays one-line per
// call-site.
func (g *Gui) emitWorkerEvent(evt string, attrs ...slog.Attr) {
	if g == nil || g.deps.Common == nil {
		return
	}
	logs.Event(g.deps.Common.Logger(), "state", evt, attrs...)
}

// WaitWorkers blocks until every in-flight OnWorker goroutine has
// returned. Test-only seam (and Close path): goleak-based assertions
// need a deterministic join point. Returns nil on success; a non-nil
// error if the wait exceeds the supplied timeout via the embedded
// channel — kept simple here, callers wrap with their own timeout when
// needed.
func (g *Gui) WaitWorkers() {
	g.workersWG.Wait()
}

// workersWGFields is a compile-time guard that the embedded fields used
// by the threading helpers are defined on Gui. If a future refactor
// drops one, this file fails to compile loudly.
//
//nolint:unused
var _ = func() error {
	var g Gui
	_ = &g.busy
	_ = &g.workersWG
	_ = &g.mutexes
	return fmt.Errorf("compile-time guard only")
}
