package common

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/afero"
)

// DebounceWindow is the idle period after the last MutateAndSave call before a
// coalesced Save fires. Exposed for tests; production code uses this default.
const DebounceWindow = 500 * time.Millisecond

// errStoreClosed is returned (via the optional error channel, and surfaced by
// MutateAndSave-after-Close in tests) when an operation is attempted on a
// closed AppStateStore.
var errStoreClosed = errors.New("appstatestore: closed")

// ErrStoreClosed reports whether err indicates the store has been closed.
// Exposed so tests + callers can do typed checks without importing the
// unexported sentinel.
func ErrStoreClosed() error { return errStoreClosed }

// Timer is the minimal *time.Timer surface needed by the store. Both
// realClock's *time.Timer and the test fake-clock timers satisfy it.
type Timer interface {
	Stop() bool
}

// Clock is the time seam used by AppStateStore. Production callers should use
// the result of DefaultClock(); tests inject a fake clock for deterministic
// debounce assertions. Per epic post-review notes (M06e / Adv-B X14) this is a
// named Handoff Artifact consumed by T11 for goroutine-leak / Flush tests.
type Clock interface {
	Now() time.Time
	// AfterFunc schedules fn to run after d. The returned Timer must support
	// Stop() that returns true if the call stopped the timer before fn ran.
	AfterFunc(d time.Duration, fn func()) Timer
}

// realClock is the production Clock backed by the stdlib time package.
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) AfterFunc(d time.Duration, fn func()) Timer {
	return time.AfterFunc(d, fn)
}

// DefaultClock returns the real wall-clock Clock for production wiring.
func DefaultClock() Clock { return realClock{} }

// AppStateStore is the only safe API for concurrent mutation of AppState. It
// wraps a *AppState with a mutex and a 500ms debounced background writer.
//
// Concurrency contract:
//   - MutateAndSave/Save/Load/Flush/Close are safe to call from any goroutine.
//   - The wrapped *AppState struct itself stays yaml-pure (no embedded mutex),
//     so reflect.DeepEqual on *AppState in foundation tests keeps working.
//   - Only AppStateStore is allowed to spawn the background save goroutine —
//     downstream helpers (e.g. schemas helper / T5) must funnel mutations
//     through MutateAndSave from the main loop. See epic note D8.
//   - Save() takes a defensive deep-copy snapshot under mu, releases mu, then
//     marshals the snapshot. This eliminates the yaml.Marshal-vs-concurrent-
//     map-write panic flagged by Adv-A (M06b).
type AppStateStore struct {
	mu    sync.Mutex
	state *AppState
	fs    afero.Fs
	path  string
	clock Clock

	// Debounce coordination. timer is reset on every MutateAndSave; on fire it
	// signals saveTrigger. The background loop performs the actual write and
	// updates saveInflight so Flush can wait.
	timer Timer

	// pending is true when a MutateAndSave has scheduled a save that has not
	// yet completed (either the debounce timer is running OR a save is in
	// flight). pendingCond broadcasts when pending transitions to false so
	// Flush can wake.
	pending     bool
	pendingCond *sync.Cond

	// closed transitions to true exactly once when Close is called.
	closed atomic.Bool

	// lastSaveErr records the most recent debounced-save error. Read under mu.
	lastSaveErr error
}

// NewAppStateStore constructs a store wrapping a zero-value AppState. The
// store owns the *AppState — callers MUST NOT read or mutate fields directly;
// use MutateAndSave / Load / accessors. The background debounce machinery is
// lazy: no goroutine is spawned until the first MutateAndSave fires the timer.
func NewAppStateStore(fs afero.Fs, path string, clock Clock) *AppStateStore {
	if clock == nil {
		clock = DefaultClock()
	}
	s := &AppStateStore{
		state: &AppState{},
		fs:    fs,
		path:  path,
		clock: clock,
	}
	s.pendingCond = sync.NewCond(&s.mu)
	return s
}

// MutateAndSave runs fn while holding the store mutex, then schedules a
// coalesced background Save (500ms debounce). If the store is closed,
// MutateAndSave is a no-op and records errStoreClosed in lastSaveErr (queryable
// via LastSaveErr). It does NOT panic — per AC fix in the epic post-review
// resolutions.
func (s *AppStateStore) MutateAndSave(fn func(*AppState)) {
	if s.closed.Load() {
		s.mu.Lock()
		s.lastSaveErr = errStoreClosed
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	fn(s.state)
	// Mark pending and (re)arm the debounce timer.
	s.pending = true
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = s.clock.AfterFunc(DebounceWindow, s.debouncedFire)
	s.mu.Unlock()
}

// debouncedFire is invoked by the Clock after the debounce window elapses. It
// performs a synchronous Save on a defensive snapshot, then clears the pending
// flag and broadcasts to Flush waiters.
func (s *AppStateStore) debouncedFire() {
	// If Close happened between the timer scheduling and firing, do nothing —
	// Close already cleared pending and broadcast.
	if s.closed.Load() {
		return
	}
	err := s.saveSnapshot()
	s.mu.Lock()
	s.lastSaveErr = err
	s.pending = false
	s.pendingCond.Broadcast()
	s.mu.Unlock()
}

// saveSnapshot is the shared core: under mu, take a deep-copy snapshot of the
// AppState; release mu; yaml.Marshal + atomic write outside the lock. This is
// what eliminates the yaml.Marshal vs concurrent map-write panic (Adv-A / M06b).
func (s *AppStateStore) saveSnapshot() error {
	s.mu.Lock()
	snap := deepCopyAppState(s.state)
	fs := s.fs
	path := s.path
	s.mu.Unlock()
	return snap.Save(fs, path)
}

// Save synchronously writes the current state to disk via the snapshot
// pattern. Unlike MutateAndSave it does NOT debounce — useful for bootstrap /
// shutdown paths that want a definitive on-disk write. Safe to call
// concurrently with MutateAndSave.
func (s *AppStateStore) Save() error {
	return s.saveSnapshot()
}

// Load synchronously reads the YAML file at the store's path into the wrapped
// AppState. Intended for bootstrap; do not call concurrently with
// MutateAndSave (callers should Load() before publishing the store).
func (s *AppStateStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Load(s.fs, s.path)
}

// Flush blocks until any pending debounced Save has completed. Safe to call
// from a signal handler. If no save is pending, Flush returns immediately.
// Returns the last save error, if any.
func (s *AppStateStore) Flush() error {
	s.mu.Lock()
	for s.pending {
		s.pendingCond.Wait()
	}
	err := s.lastSaveErr
	s.mu.Unlock()
	return err
}

// Close drains any in-flight debounced Save and stops the background timer.
// After Close returns, MutateAndSave is a no-op (records errStoreClosed).
// Close is idempotent and safe to call concurrently with other operations.
func (s *AppStateStore) Close() error {
	// Set closed first so MutateAndSave callers stop arming new timers.
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	s.mu.Lock()
	if s.timer != nil {
		// If Stop returns false the timer either already fired (debouncedFire
		// is running concurrently — we'll wait via pendingCond) or was already
		// stopped. Either way the pending flag will be cleared by the firing
		// goroutine.
		if s.timer.Stop() {
			// We stopped the timer before it fired; clear pending ourselves
			// and broadcast for any Flush waiters.
			s.pending = false
			s.pendingCond.Broadcast()
		}
		s.timer = nil
	} else {
		// No timer ever armed; ensure waiters are released.
		s.pending = false
		s.pendingCond.Broadcast()
	}
	// Wait for any in-flight debouncedFire to drain.
	for s.pending {
		s.pendingCond.Wait()
	}
	err := s.lastSaveErr
	s.mu.Unlock()
	return err
}

// LastSaveErr returns the most recent save error (debounced or synchronous).
// Returns nil if the last save succeeded or no save has been attempted.
func (s *AppStateStore) LastSaveErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastSaveErr
}

// IsStartupTipsSeen returns true iff StartupTipsSeenAt is not the zero time.
// Per the AC fix in the epic post-review notes this uses !t.IsZero(), which
// is the CORRECT Go idiom (time.Time is a struct; != on the zero value is
// not idiomatic and depends on monotonic-clock bits).
func (s *AppStateStore) IsStartupTipsSeen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.state.StartupTipsSeenAt.IsZero()
}

// StampStartupTips sets StartupTipsSeenAt = clock.Now() and triggers a
// debounced save. Idempotent — calling twice still records the LATEST
// timestamp (which is what the IsStartupTipsSeen check cares about).
func (s *AppStateStore) StampStartupTips() {
	now := s.clock.Now()
	s.MutateAndSave(func(a *AppState) {
		a.StartupTipsSeenAt = now
	})
}

// HiddenColumnsSnapshot returns a defensive copy of the persisted hidden-column
// name list for the given (connID, baseTable) pair. Returns nil when no entry
// exists. Caller may mutate the returned slice. dbsavvy-uv0.6.
func (s *AppStateStore) HiddenColumnsSnapshot(connID, baseTable string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.HiddenColumns == nil {
		return nil
	}
	inner, ok := s.state.HiddenColumns[connID]
	if !ok {
		return nil
	}
	src, ok := inner[baseTable]
	if !ok {
		return nil
	}
	out := make([]string, len(src))
	copy(out, src)
	return out
}

// LastResultViewModeSnapshot returns the persisted result-grid view mode
// ("grid" / "expanded"). Empty string means the user has never toggled.
// Callers should normalise empty / unknown values to "grid" on read.
// dbsavvy-uv0.7.
func (s *AppStateStore) LastResultViewModeSnapshot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.LastResultViewMode
}

// SetLastResultViewMode persists the result-grid view mode through the
// debounced save path. Idempotent. dbsavvy-uv0.7.
func (s *AppStateStore) SetLastResultViewMode(m string) {
	s.MutateAndSave(func(a *AppState) {
		a.LastResultViewMode = m
	})
}

// HiddenSchemasSnapshot returns a defensive copy of the hidden-schemas slice
// for the given connection ID. Callers may mutate the returned slice without
// affecting store state. Returns nil if no entry exists.
func (s *AppStateStore) HiddenSchemasSnapshot(connectionID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.HiddenSchemas == nil {
		return nil
	}
	src, ok := s.state.HiddenSchemas[connectionID]
	if !ok {
		return nil
	}
	out := make([]string, len(src))
	copy(out, src)
	return out
}

// deepCopyAppState produces a defensive copy of a so that yaml.Marshal can
// iterate its maps without racing concurrent mutators on the original. All
// six map fields plus the slice field are copied; scalar fields are copied by
// assignment. Cost is ~6 small-map walks per Save which is well within the
// debounce budget.
func deepCopyAppState(a *AppState) *AppState {
	if a == nil {
		return &AppState{}
	}
	cp := &AppState{
		LastConnectionID:   a.LastConnectionID,
		LastTheme:          a.LastTheme,
		LastResultViewMode: a.LastResultViewMode,
		StartupTipsSeenAt:  a.StartupTipsSeenAt,
		Version:            a.Version,
	}
	if a.RecentConnectionIDs != nil {
		cp.RecentConnectionIDs = make([]string, len(a.RecentConnectionIDs))
		copy(cp.RecentConnectionIDs, a.RecentConnectionIDs)
	}
	if a.LastBufferUUIDs != nil {
		cp.LastBufferUUIDs = make(map[string]string, len(a.LastBufferUUIDs))
		for k, v := range a.LastBufferUUIDs {
			cp.LastBufferUUIDs[k] = v
		}
	}
	if a.StatementTimeoutOverride != nil {
		cp.StatementTimeoutOverride = make(map[string]string, len(a.StatementTimeoutOverride))
		for k, v := range a.StatementTimeoutOverride {
			cp.StatementTimeoutOverride[k] = v
		}
	}
	if a.HiddenSchemas != nil {
		cp.HiddenSchemas = make(map[string][]string, len(a.HiddenSchemas))
		for k, v := range a.HiddenSchemas {
			dst := make([]string, len(v))
			copy(dst, v)
			cp.HiddenSchemas[k] = dst
		}
	}
	if a.HiddenColumns != nil {
		cp.HiddenColumns = make(map[string]map[string][]string, len(a.HiddenColumns))
		for k, inner := range a.HiddenColumns {
			cpInner := make(map[string][]string, len(inner))
			for ik, iv := range inner {
				dst := make([]string, len(iv))
				copy(dst, iv)
				cpInner[ik] = dst
			}
			cp.HiddenColumns[k] = cpInner
		}
	}
	if a.LastSessionSettings != nil {
		cp.LastSessionSettings = make(map[string]map[string]string, len(a.LastSessionSettings))
		for k, inner := range a.LastSessionSettings {
			cpInner := make(map[string]string, len(inner))
			for ik, iv := range inner {
				cpInner[ik] = iv
			}
			cp.LastSessionSettings[k] = cpInner
		}
	}
	return cp
}
