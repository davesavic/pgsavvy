package keys

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// DispatchResult classifies the outcome of Matcher.Dispatch.
//
// Dispatched   the matched leaf's Handler was invoked and Matcher state
//
//	was reset.
//
// Pending      the key extended a partial match (prefix or ambiguous
//
//	leaf); the Matcher is now waiting for the next key or a
//	timeout.
//
// FellThrough  the key did not match any binding in the scope OR global
//
//	trie for the active mode. Caller (master Editor in
//	dlp.8) decides whether to forward to View.Editor.Edit or
//	swallow.
//
// Cancelled    reserved: Matcher.Cancel was driven by the dispatch
//
//	path (e.g. <esc> on a partial). Not currently emitted by
//	Dispatch; Cancel() consumers should read IsPartial.
//
// Swallowed    reserved: future use for explicit `<nop>` leaf hits
//
//	that consume the key but produce no action.
//
// Passthrough  ModeInsert / ModeCommand received a printable rune that
//
//	does NOT match any binding AND no partial match is in
//	flight. Signals to the master Editor (dlp.8b) that the
//	rune should be forwarded to gocui.DefaultEditor.
type DispatchResult int

const (
	Dispatched DispatchResult = iota
	Pending
	FellThrough
	Cancelled
	Swallowed
	Passthrough
)

// String returns a stable lowercase label for r (used in tests/logs).
func (r DispatchResult) String() string {
	switch r {
	case Dispatched:
		return "dispatched"
	case Pending:
		return "pending"
	case FellThrough:
		return "fell_through"
	case Cancelled:
		return "cancelled"
	case Swallowed:
		return "swallowed"
	case Passthrough:
		return "passthrough"
	default:
		return fmt.Sprintf("dispatch_result(%d)", int(r))
	}
}

// countMax clamps accumulated counts so a stuck-key avalanche of digits
// cannot wrap an int. 999999 mirrors the documented overflow guard
// (acceptance criterion "count overflow").
const countMax = 999999

// InsertPendingFlush is the callback shape master Editor (dlp.8b)
// registers with the Matcher. The Matcher invokes it from the timer
// goroutine when a partial sequence in ModeInsert times out without
// resolving to a leaf, so the buffered runes can be written to the
// TextArea (per D16).
type InsertPendingFlush = func(scope types.ContextKey, runes []rune)

// Matcher is the per-process keystroke dispatcher. One Matcher instance
// is shared by every Context; the active scope is supplied per
// Dispatch call.
//
// Concurrency: every read/write of internal state goes through m.mu.
// The TrieSet is held in an atomic.Pointer so SwapTrieSet can publish a
// new snapshot without taking m.mu for the store itself (Cancel runs
// first, under the mutex, to drop any in-flight pending state).
type Matcher struct {
	trieSet atomic.Pointer[TrieSet]
	modes   *ModeStore
	leader  rune

	tlen   time.Duration
	ttlen  time.Duration
	wdelay time.Duration

	registers *RegisterStore
	whichkey  WhichKeyNotifier
	log       DebugLogger

	mu       sync.Mutex
	pending  []Key
	lastLeaf *commands.Command
	count    int
	register rune
	timer    *time.Timer
	seq      uint64 // monotonic; stale timer/AfterFunc fires compare against this

	flushMu      sync.RWMutex
	insertFlush  InsertPendingFlush
	timerScope   types.ContextKey
	timerMode    types.Mode
	timerPending []Key
}

// MatcherConfig groups Matcher construction parameters.
type MatcherConfig struct {
	Modes         *ModeStore
	Leader        rune
	TimeoutLen    time.Duration
	TtimeoutLen   time.Duration
	WhichKeyDelay time.Duration
	Registers     *RegisterStore
	WhichKey      WhichKeyNotifier
	Log           DebugLogger
}

// NewMatcher constructs a Matcher with the supplied configuration and
// initial TrieSet. The TrieSet may be nil (the Matcher behaves as if
// every Dispatch fell through).
//
// Returns an error if cfg.Leader is a digit rune ('0'..'9'). Such a
// leader would make count-collection ambiguous (per AC "Matcher refuses
// to start if leader is a single digit"); dlp.3 already rejects this at
// config validation, the Matcher refuses defensively.
func NewMatcher(initial *TrieSet, cfg MatcherConfig) (*Matcher, error) {
	if cfg.Leader >= '0' && cfg.Leader <= '9' {
		return nil, fmt.Errorf("keys: leader %q is a digit; counts would be ambiguous", cfg.Leader)
	}
	modes := cfg.Modes
	if modes == nil {
		modes = NewModeStore()
	}
	regs := cfg.Registers
	if regs == nil {
		regs = NewRegisterStore()
	}
	m := &Matcher{
		modes:     modes,
		leader:    cfg.Leader,
		tlen:      cfg.TimeoutLen,
		ttlen:     cfg.TtimeoutLen,
		wdelay:    cfg.WhichKeyDelay,
		registers: regs,
		whichkey:  cfg.WhichKey,
		log:       cfg.Log,
	}
	m.trieSet.Store(initial)
	return m, nil
}

// IsPartial reports whether the Matcher currently holds a pending
// sequence. Test-only accessor mirroring OneshotArm.IsArmed.
func (m *Matcher) IsPartial() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pending) > 0 || m.count != 0 || m.register != 0
}

// SwapTrieSet publishes a new TrieSet atomically. Any in-flight pending
// state is cancelled (D9: cancel before atomic swap) so a partial chord
// CANNOT cross a reload boundary.
func (m *Matcher) SwapTrieSet(t *TrieSet) {
	m.Cancel()
	m.trieSet.Store(t)
}

// TrieSet returns the currently-published TrieSet snapshot. Cheatsheet
// (dlp.10) and options-bar (dlp.12) callers read the live trie to
// enumerate bindings. Returns nil when no TrieSet has been published
// yet; callers MUST nil-check.
func (m *Matcher) TrieSet() *TrieSet {
	return m.trieSet.Load()
}

// OnInsertPendingFlush registers the master Editor's pending-buffer
// flush callback. fn may be nil to clear a previous registration.
// Concurrency-safe.
func (m *Matcher) OnInsertPendingFlush(fn InsertPendingFlush) {
	m.flushMu.Lock()
	m.insertFlush = fn
	m.flushMu.Unlock()
}

// CurrentMode returns the Mode currently recorded for scope. The master
// Editor (dlp.8b) calls this to decide whether a Passthrough result
// should delegate to gocui.DefaultEditor (ModeInsert / ModeCommand) or
// be dropped (other modes).
func (m *Matcher) CurrentMode(scope types.ContextKey) types.Mode {
	return m.modes.Get(scope)
}

// Registers returns the underlying RegisterStore. Handlers that need to
// read/write vim registers reach them through this accessor.
func (m *Matcher) Registers() *RegisterStore {
	return m.registers
}

// Cancel drops every piece of pending state synchronously:
//   - stops the timer
//   - clears pending / lastLeaf / count / register
//   - increments seq so any in-flight AfterFunc callback is a no-op
//   - notifies the WhichKey (Hide is invoked OUTSIDE m.mu)
//
// Safe to call when idle.
func (m *Matcher) Cancel() {
	m.mu.Lock()
	wasPartial := len(m.pending) > 0
	m.cancelLocked()
	m.mu.Unlock()
	if wasPartial && m.whichkey != nil {
		m.whichkey.Hide()
	}
}

// cancelLocked clears state. Caller MUST hold m.mu. Does NOT call
// WhichKey.Hide (that lives outside the mutex per the mu-release
// invariant).
func (m *Matcher) cancelLocked() {
	if m.timer != nil {
		m.timer.Stop()
		m.timer = nil
	}
	m.pending = nil
	m.lastLeaf = nil
	m.count = 0
	m.register = 0
	m.timerPending = nil
	m.seq++
}

// Dispatch consumes one keypress. Returns the resulting DispatchResult
// plus any error produced by an invoked Handler.
//
// IMPORTANT: m.mu is released BEFORE Handler invocation (mirrors
// oneshotarm.go Dispatch). A Handler that re-enters the Matcher via
// Cancel / SwapTrieSet therefore does NOT deadlock.
func (m *Matcher) Dispatch(scope types.ContextKey, k Key) (DispatchResult, error) {
	mode := m.modes.Get(scope)

	// Insert-mode fast path: a printable rune that doesn't match any
	// binding in (mode, scope) or (mode, GLOBAL) AND no partial is in
	// flight is forwarded to the master Editor as passthrough. Count
	// collection is DISABLED in ModeInsert (a digit is text, not a
	// count).
	if mode == types.ModeInsert || mode == types.ModeCommand {
		m.mu.Lock()
		empty := len(m.pending) == 0 && m.count == 0 && m.register == 0
		m.mu.Unlock()
		if empty && isPrintableRune(k) {
			ts := m.trieSet.Load()
			if !bindingExistsAt(ts, mode, scope, k) && !bindingExistsAt(ts, mode, types.GLOBAL, k) {
				return Passthrough, nil
			}
		}
	}

	m.mu.Lock()

	// Register prefix: when idle and the previous key was `"`, the next
	// key is the register name. We model this as a one-key buffer:
	// pending starts as [`"`] and the next call replaces m.register
	// then resets pending.
	if len(m.pending) == 1 && m.pending[0].Special == KeyNone && m.pending[0].Code == '"' && m.pending[0].Mod == 0 {
		// Only accept rune register names; anything else cancels.
		if k.Special == KeyNone && k.Mod == 0 && k.Code != 0 {
			m.register = k.Code
			m.pending = nil
			m.lastLeaf = nil
			if m.timer != nil {
				m.timer.Stop()
				m.timer = nil
			}
			m.seq++
			m.mu.Unlock()
			return Pending, nil
		}
		// Non-rune key on a register prompt: cancel silently and
		// continue with this key as a fresh dispatch.
		m.cancelLocked()
		// fall through to normal dispatch below.
	}

	// Count collection (Normal / Visual modes only). Insert/Command
	// already returned above.
	if mode != types.ModeInsert && mode != types.ModeCommand {
		if len(m.pending) == 0 && k.Special == KeyNone && k.Mod == 0 {
			isFirstCountDigit := k.Code >= '1' && k.Code <= '9'
			isContinuingCountDigit := m.count > 0 && k.Code >= '0' && k.Code <= '9'
			if isFirstCountDigit || isContinuingCountDigit {
				ts := m.trieSet.Load()
				// If the digit is itself a leaf in scope or global for
				// this mode, prefer the binding over count collection.
				if !bindingExistsAt(ts, mode, scope, k) && !bindingExistsAt(ts, mode, types.GLOBAL, k) {
					next := m.count*10 + int(k.Code-'0')
					if next > countMax {
						next = countMax
					}
					m.count = next
					m.mu.Unlock()
					return Pending, nil
				}
			}
		}
	}

	// Detect register-prefix START: idle (no pending), key is `"`,
	// and there is no binding for `"` at (mode, scope) or (mode, GLOBAL).
	// This guards against trampling a user-defined `"` binding.
	if mode != types.ModeInsert && mode != types.ModeCommand &&
		len(m.pending) == 0 && k.Special == KeyNone && k.Mod == 0 && k.Code == '"' {
		ts := m.trieSet.Load()
		if !bindingExistsAt(ts, mode, scope, k) && !bindingExistsAt(ts, mode, types.GLOBAL, k) {
			m.pending = []Key{k}
			m.seq++
			m.mu.Unlock()
			return Pending, nil
		}
	}

	// Attempt 1: scope-specific trie with current pending + k.
	ts := m.trieSet.Load()
	scopeTrie, _ := ts.Get(mode, scope)

	if scopeTrie != nil {
		seq := append(append([]Key(nil), m.pending...), k)
		res := scopeTrie.Lookup(seq)
		if res.Found {
			return m.handleLookup(res, seq, scope, mode)
		}
		// Not found in scope WITH pending — clear scope pending and try
		// global with k FRESH (D10 scope→global fall-through). If
		// pending was non-empty we drop it now.
		if len(m.pending) > 0 {
			m.cancelPendingLocked()
		}
	}

	// Attempt 2: global trie with k FRESH.
	globalTrie, _ := ts.Get(mode, types.GLOBAL)
	if globalTrie != nil {
		seq := []Key{k}
		res := globalTrie.Lookup(seq)
		if res.Found {
			return m.handleLookup(res, seq, types.GLOBAL, mode)
		}
	}

	// Step 3: no match. ModeInsert / ModeCommand with a printable rune
	// → Passthrough. Otherwise FellThrough. (The fast-path above
	// already covered the empty-pending case; here we may have had a
	// partial that just got cancelled.)
	if (mode == types.ModeInsert || mode == types.ModeCommand) && isPrintableRune(k) {
		m.mu.Unlock()
		return Passthrough, nil
	}

	// Drop any count/register that was sitting in front of an unmatched
	// key (no carryover per AC).
	hadPartial := m.count != 0 || m.register != 0
	if hadPartial {
		m.cancelLocked()
	}
	m.mu.Unlock()
	return FellThrough, nil
}

// handleLookup resolves a Lookup that returned Found=true. The caller
// holds m.mu; this method releases m.mu BEFORE invoking the Handler.
func (m *Matcher) handleLookup(res LookupResult, seq []Key, scope types.ContextKey, mode types.Mode) (DispatchResult, error) {
	switch {
	case res.IsLeaf && !res.HasChildren:
		// Unambiguous leaf: fire immediately.
		cmd := res.Action
		count := m.count
		reg := m.register
		m.cancelLocked()
		m.mu.Unlock()
		if m.whichkey != nil {
			m.whichkey.Hide()
		}
		return m.invokeHandler(cmd, scope, mode, count, reg)

	case res.IsLeaf && res.HasChildren:
		// Ambiguous: leaf AND prefix. Buffer and schedule timer.
		m.pending = seq
		m.lastLeaf = res.Action
		m.scheduleTimerLocked(scope, mode)
		m.notifyWhichKeyLocked(scope, seq)
		m.mu.Unlock()
		return Pending, nil

	case !res.IsLeaf && res.HasChildren:
		// Interior node: pure prefix. Buffer; schedule inactivity timer
		// (so an abandoned prefix doesn't sit forever); notify which-key.
		m.pending = seq
		m.lastLeaf = nil
		m.scheduleTimerLocked(scope, mode)
		m.notifyWhichKeyLocked(scope, seq)
		m.mu.Unlock()
		return Pending, nil

	default:
		// res.Found but neither leaf nor children — root node from an
		// empty lookup. Should not be reachable from Dispatch (k is
		// always appended); treat as fall-through.
		m.mu.Unlock()
		return FellThrough, nil
	}
}

// invokeHandler runs cmd.Handler with the supplied ExecCtx. Panics are
// recovered identically here AND on the timer-fire path; a recovered
// panic is returned as an error and Matcher state has already been
// reset by the caller.
func (m *Matcher) invokeHandler(cmd *commands.Command, scope types.ContextKey, mode types.Mode, count int, reg rune) (result DispatchResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			id := "<unknown>"
			if cmd != nil {
				id = cmd.ID
			}
			err = fmt.Errorf("panic in handler %s: %v", id, r)
			result = Dispatched
		}
	}()
	if cmd == nil || cmd.Handler == nil {
		return Dispatched, nil
	}
	ctx := commands.ExecCtx{
		Count:    count,
		Register: reg,
		Mode:     mode,
		Scope:    scope,
	}
	if err := cmd.Handler(ctx); err != nil {
		return Dispatched, err
	}
	return Dispatched, nil
}

// scheduleTimerLocked starts the inactivity / leaf-ambiguity timer.
// Uses ttlen if pending[0] is <esc>, else tlen. Caller MUST hold m.mu.
func (m *Matcher) scheduleTimerLocked(scope types.ContextKey, mode types.Mode) {
	if m.timer != nil {
		m.timer.Stop()
		m.timer = nil
	}
	delay := m.tlen
	if len(m.pending) > 0 && m.pending[0].Special == KeyEsc {
		delay = m.ttlen
	}
	if delay <= 0 {
		return
	}
	m.seq++
	id := m.seq
	m.timerScope = scope
	m.timerMode = mode
	m.timerPending = append([]Key(nil), m.pending...)
	m.timer = time.AfterFunc(delay, func() {
		m.onTimerFire(id)
	})
}

// onTimerFire runs on its own goroutine (time.AfterFunc). Compares
// captured id against the current m.seq; ignores stale fires. Releases
// m.mu before invoking the leaf Handler / insert-pending flush.
func (m *Matcher) onTimerFire(id uint64) {
	defer func() {
		// Timer goroutine recover: a panicking handler must not crash
		// the Matcher's owning goroutine. Logged via DebugLogger when
		// present.
		if r := recover(); r != nil {
			if m.log != nil {
				m.log.Debugf("matcher: timer-fire handler panic: %v", r)
			}
		}
	}()

	m.mu.Lock()
	if id != m.seq {
		m.mu.Unlock()
		return
	}
	leaf := m.lastLeaf
	scope := m.timerScope
	mode := m.timerMode
	count := m.count
	reg := m.register
	pendingCopy := append([]Key(nil), m.timerPending...)
	m.cancelLocked()
	m.mu.Unlock()

	if m.whichkey != nil {
		m.whichkey.Hide()
	}

	if leaf != nil {
		// Ambiguous-leaf timeout: fire the leaf action.
		_, _ = m.invokeHandler(leaf, scope, mode, count, reg)
		return
	}

	// No leaf: in ModeInsert, deliver buffered runes to the master
	// Editor via the flush callback (per D16). In other modes the
	// pending is silently dropped.
	if mode == types.ModeInsert && len(pendingCopy) > 0 {
		m.flushMu.RLock()
		fn := m.insertFlush
		m.flushMu.RUnlock()
		if fn != nil {
			runes := make([]rune, 0, len(pendingCopy))
			for _, k := range pendingCopy {
				if k.Special == KeyNone && k.Mod == 0 && k.Code != 0 {
					runes = append(runes, k.Code)
				}
			}
			if len(runes) > 0 {
				fn(scope, runes)
			}
		}
	}
}

// cancelPendingLocked drops the pending sequence (and stops the timer)
// but RETAINS count/register so the scope→global fall-through can carry
// the count forward. Caller holds m.mu.
func (m *Matcher) cancelPendingLocked() {
	if m.timer != nil {
		m.timer.Stop()
		m.timer = nil
	}
	m.pending = nil
	m.lastLeaf = nil
	m.timerPending = nil
	m.seq++
}

// notifyWhichKeyLocked invokes WhichKey.ShowAfter for the current
// partial. Called under m.mu — implementations of WhichKeyNotifier MUST
// not synchronously re-enter Matcher (the interface doc reinforces
// this). For absolute safety against Hide() racing Show(), we copy the
// prefix before the call.
func (m *Matcher) notifyWhichKeyLocked(scope types.ContextKey, prefix []Key) {
	if m.whichkey == nil || m.wdelay <= 0 {
		return
	}
	copyPrefix := append([]Key(nil), prefix...)
	m.whichkey.ShowAfter(m.wdelay, scope, copyPrefix)
}

// bindingExistsAt reports whether a leaf or interior binding exists at
// the single-key sequence [k] in ts[(mode, scope)].
func bindingExistsAt(ts *TrieSet, mode types.Mode, scope types.ContextKey, k Key) bool {
	if ts == nil {
		return false
	}
	t, ok := ts.Get(mode, scope)
	if !ok || t == nil {
		return false
	}
	res := t.Lookup([]Key{k})
	return res.Found
}

// isPrintableRune reports whether k carries a printable bare rune
// (no modifiers, no special key). Used by the Insert/Command
// passthrough fast path.
func isPrintableRune(k Key) bool {
	if k.Special != KeyNone {
		return false
	}
	if k.Mod != 0 {
		return false
	}
	if k.Code == 0 {
		return false
	}
	return unicode.IsPrint(k.Code)
}
