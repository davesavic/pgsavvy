package common

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
)

// --- fake clock --------------------------------------------------------------

// fakeTimer is a Timer whose firing is controlled by the fake clock's Advance.
type fakeTimer struct {
	clk      *fakeClock
	deadline time.Time
	fn       func()
	stopped  bool
	fired    bool
	idx      int // index into clk.timers
}

func (t *fakeTimer) Stop() bool {
	t.clk.mu.Lock()
	defer t.clk.mu.Unlock()
	if t.fired || t.stopped {
		return false
	}
	t.stopped = true
	return true
}

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(1_700_000_000, 0)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, fn func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{
		clk:      c,
		deadline: c.now.Add(d),
		fn:       fn,
		idx:      len(c.timers),
	}
	c.timers = append(c.timers, t)
	return t
}

// Advance moves the clock forward by d, firing (synchronously, in order) any
// non-stopped timer whose deadline is reached. Timer fn is invoked WITHOUT
// holding c.mu so re-entrant AfterFunc calls from fn work.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	due := []*fakeTimer{}
	for _, t := range c.timers {
		if t.stopped || t.fired {
			continue
		}
		if !t.deadline.After(c.now) {
			t.fired = true
			due = append(due, t)
		}
	}
	c.mu.Unlock()
	for _, t := range due {
		t.fn()
	}
}

// --- tests ------------------------------------------------------------------

func TestStoreRapidMashCoalesces(t *testing.T) {
	fs := afero.NewMemMapFs()
	clk := newFakeClock()
	const path = "/state.yml"

	// Recording Fs: count Rename calls (atomic-rename is the commit point).
	rec := &recordingFs{Fs: fs}
	s := NewAppStateStore(rec, path, clk)
	defer func() { _ = s.Close() }()

	// 50 mutations in a tight loop, all within "100ms" of fake time. Advance
	// clock 1ms between each so timers don't all share an identical deadline
	// (production usage spaces these via real keystrokes).
	for i := 0; i < 50; i++ {
		idx := i
		s.MutateAndSave(func(a *AppState) {
			if a.HiddenSchemas == nil {
				a.HiddenSchemas = map[string][]string{}
			}
			a.HiddenSchemas["conn"] = []string{"v" + intToStr(idx)}
		})
		clk.Advance(1 * time.Millisecond)
	}
	// Total elapsed so far: 50ms. No save should have fired yet (debounce is
	// 500ms after the LAST mutation).
	require.Equal(t, int32(0), atomic.LoadInt32(&rec.renames), "no save expected before debounce window elapses")

	// Advance past the 500ms debounce window.
	clk.Advance(DebounceWindow + 10*time.Millisecond)

	// Flush in case the debounced fire is still draining (it's synchronous in
	// our fake clock so this is effectively a no-op, but assert the contract).
	require.NoError(t, s.Flush())

	require.Equal(t, int32(1), atomic.LoadInt32(&rec.renames), "exactly one Save expected after debounce")

	// On-disk state reflects the LAST mutation.
	b := &AppState{}
	require.NoError(t, b.Load(fs, path))
	require.Equal(t, []string{"v49"}, b.HiddenSchemas["conn"])
}

func TestCloseDrainsInFlight(t *testing.T) {
	// Capture goroutine count baseline.
	before := runtime.NumGoroutine()

	fs := afero.NewMemMapFs()
	clk := newFakeClock()
	s := NewAppStateStore(fs, "/state.yml", clk)

	s.MutateAndSave(func(a *AppState) { a.LastConnectionID = "x" })

	deadline := time.Now().Add(100 * time.Millisecond)
	require.NoError(t, s.Close())
	require.True(t, time.Now().Before(deadline), "Close should return within 100ms")

	// After Close, MutateAndSave is a no-op (and records errStoreClosed).
	s.MutateAndSave(func(a *AppState) { a.LastConnectionID = "y" })
	require.ErrorIs(t, s.LastSaveErr(), errStoreClosed)

	// Idempotent.
	require.NoError(t, s.Close())

	// Goroutine hygiene: AppStateStore must not leak background goroutines.
	// We don't use go.uber.org/goleak yet (lands in T11) — assert NumGoroutine
	// returns to baseline within a tolerance.
	runtime.Gosched()
	after := runtime.NumGoroutine()
	require.LessOrEqual(t, after, before+1, "no goroutine leak: before=%d after=%d", before, after)
}

func TestSaveSnapshotIsolation(t *testing.T) {
	// Concurrent MutateAndSave + background synchronous Save must be -race
	// clean. The snapshot pattern in saveSnapshot is what makes this safe:
	// yaml.Marshal sees a defensive deep copy, not the live state.
	fs := afero.NewMemMapFs()
	clk := newFakeClock()
	s := NewAppStateStore(fs, "/state.yml", clk)
	defer func() { _ = s.Close() }()

	var wg sync.WaitGroup
	const iters = 200

	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			idx := i
			s.MutateAndSave(func(a *AppState) {
				if a.HiddenSchemas == nil {
					a.HiddenSchemas = map[string][]string{}
				}
				a.HiddenSchemas["k"] = []string{"v" + intToStr(idx)}
			})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			_ = s.Save()
		}
	}()
	wg.Wait()
}

func TestFlushWaitsForPendingSave(t *testing.T) {
	// With the fake clock the debouncedFire is invoked synchronously by
	// clk.Advance — so Flush after Advance is trivially satisfied. Verify the
	// contract: with NO Advance, Flush blocks until... we Close (which clears
	// pending). Use a goroutine + channel to assert ordering.
	fs := afero.NewMemMapFs()
	clk := newFakeClock()
	s := NewAppStateStore(fs, "/state.yml", clk)

	s.MutateAndSave(func(a *AppState) { a.LastConnectionID = "x" })

	done := make(chan struct{})
	go func() {
		_ = s.Flush()
		close(done)
	}()

	// Give the goroutine a moment to enter Flush.
	runtime.Gosched()

	select {
	case <-done:
		t.Fatal("Flush returned before pending save fired")
	default:
	}

	// Advance the clock past the debounce window; the timer fires
	// synchronously inside Advance, marks pending=false, broadcasts.
	clk.Advance(DebounceWindow + time.Millisecond)

	select {
	case <-done:
		// Expected: Flush released after debouncedFire cleared pending.
	case <-time.After(time.Second):
		t.Fatal("Flush did not return after debounce window elapsed")
	}

	// Subsequent Flush returns immediately.
	require.NoError(t, s.Flush())
	require.NoError(t, s.Close())
}

func TestIsStartupTipsSeenAndStamp(t *testing.T) {
	fs := afero.NewMemMapFs()
	clk := newFakeClock()
	s := NewAppStateStore(fs, "/state.yml", clk)
	defer func() { _ = s.Close() }()

	require.False(t, s.IsStartupTipsSeen(), "fresh store: tips not seen")

	s.StampStartupTips()
	require.True(t, s.IsStartupTipsSeen(), "after StampStartupTips: tips seen")

	// StampStartupTips schedules a debounced save; advance the clock so it
	// fires, then verify on-disk state.
	clk.Advance(DebounceWindow + time.Millisecond)
	require.NoError(t, s.Flush())

	b := &AppState{}
	require.NoError(t, b.Load(fs, "/state.yml"))
	require.False(t, b.StartupTipsSeenAt.IsZero(), "persisted timestamp non-zero")
}

func TestMutateAfterCloseReturnsErr(t *testing.T) {
	fs := afero.NewMemMapFs()
	clk := newFakeClock()
	s := NewAppStateStore(fs, "/state.yml", clk)

	require.NoError(t, s.Close())

	// Should not panic; should set lastSaveErr.
	s.MutateAndSave(func(a *AppState) { a.LastConnectionID = "x" })
	require.ErrorIs(t, s.LastSaveErr(), errStoreClosed)
	require.ErrorIs(t, ErrStoreClosed(), errStoreClosed)
}

// --- helpers ----------------------------------------------------------------

// recordingFs counts atomic-rename calls so a test can assert "exactly N
// successful Save() commits hit the disk". All other operations delegate to
// the embedded base.
type recordingFs struct {
	afero.Fs
	renames int32
}

func (r *recordingFs) Rename(oldname, newname string) error {
	atomic.AddInt32(&r.renames, 1)
	return r.Fs.Rename(oldname, newname)
}

func (r *recordingFs) Name() string { return "recordingFs" }

// intToStr is a tiny stdlib-free int formatter to keep the test file's import
// surface minimal (we already pull in testify + afero).
func intToStr(i int) string {
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	var buf [20]byte
	n := len(buf)
	for i > 0 {
		n--
		buf[n] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		n--
		buf[n] = '-'
	}
	return string(buf[n:])
}
