package orchestrator

import "time"

// spinnerTickInterval is the wall-clock period of the busy-spinner
// re-render ticker (U8). The spinner frame counter advances one step per
// interval, so the braille glyph cycles roughly ten frames per second
// while any background work is in flight.
const spinnerTickInterval = 100 * time.Millisecond

// Clock is the injectable wall-clock seam used by the busy-spinner
// animation (U8). Production wiring uses realClock (time.Now / a real
// time.Ticker); tests inject a fake that advances Now() manually and
// drives ticks deterministically.
//
// It is the FIRST clock/ticker seam in the GUI — the spinner ticker is
// the first periodic goroutine in pkg/gui, so the abstraction lives here
// in orchestrator/ to avoid cross-package coupling.
type Clock interface {
	// Now returns the current wall-clock time. The spinner frame counter
	// is computed from the elapsed time since the ticker was armed.
	Now() time.Time
	// NewTicker returns a Ticker that delivers on its channel every d.
	NewTicker(d time.Duration) Ticker
}

// Ticker abstracts time.Ticker so tests can fire ticks on demand. Stop
// must be idempotent-safe to call from the arm/stop critical section.
type Ticker interface {
	// Chan returns the receive-only tick channel.
	Chan() <-chan time.Time
	// Stop halts tick delivery and releases the underlying resources.
	Stop()
}

// realClock is the production Clock backed by the standard library.
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) NewTicker(d time.Duration) Ticker {
	return &realTicker{t: time.NewTicker(d)}
}

// realTicker adapts *time.Ticker to the Ticker interface.
type realTicker struct{ t *time.Ticker }

func (r *realTicker) Chan() <-chan time.Time { return r.t.C }
func (r *realTicker) Stop()                  { r.t.Stop() }
