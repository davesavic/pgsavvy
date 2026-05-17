package keys

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// --- test helpers -----------------------------------------------------

// buildTrieSet constructs a TrieSet from a small literal description.
// entries is a slice of (mode, scope, sequence, command) tuples.
type trieEntry struct {
	mode  types.Mode
	scope types.ContextKey
	seq   []Key
	cmd   *commands.Command
}

func buildTrieSet(t *testing.T, entries []trieEntry) *TrieSet {
	t.Helper()
	builders := map[TrieSetKey]*TrieBuilder{}
	for _, e := range entries {
		k := TrieSetKey{Mode: e.mode, Scope: e.scope}
		b, ok := builders[k]
		if !ok {
			b = NewTrieBuilder()
			builders[k] = b
		}
		b.InsertDefault(&ChordBinding{
			Sequence: e.seq,
			Mode:     e.mode,
			Scope:    e.scope,
			ActionID: e.cmd.ID,
			Source:   ShippedDefault,
			Origin:   "test",
		}, e.cmd)
	}
	out := &TrieSet{tries: map[TrieSetKey]*ChordTrie{}}
	for k, b := range builders {
		trie, _ := b.Build()
		out.tries[k] = trie
	}
	return out
}

// recordingCmd builds a Command whose Handler appends the supplied id to
// fired and returns the supplied error.
func recordingCmd(id string, fired *[]string, mu *sync.Mutex, err error) *commands.Command {
	return &commands.Command{
		ID: id,
		Handler: func(ctx commands.ExecCtx) error {
			mu.Lock()
			*fired = append(*fired, id)
			mu.Unlock()
			return err
		},
	}
}

// recordingCmdWithCtx captures every ExecCtx the handler receives.
func recordingCmdWithCtx(id string, gotCtx *commands.ExecCtx, mu *sync.Mutex) *commands.Command {
	return &commands.Command{
		ID: id,
		Handler: func(ctx commands.ExecCtx) error {
			mu.Lock()
			*gotCtx = ctx
			mu.Unlock()
			return nil
		},
	}
}

func keyOf(r rune) Key            { return Key{Code: r} }
func specialKey(s SpecialKey) Key { return Key{Special: s} }

// shortMatcher builds a Matcher with short timeouts suitable for tests.
func shortMatcher(t *testing.T, ts *TrieSet, scope types.ContextKey, mode types.Mode) *Matcher {
	t.Helper()
	store := NewModeStore()
	store.Set(scope, mode)
	m, err := NewMatcher(ts, MatcherConfig{
		Modes:         store,
		TimeoutLen:    30 * time.Millisecond,
		TtimeoutLen:   10 * time.Millisecond,
		WhichKeyDelay: 0, // disabled in most tests
	})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	return m
}

// waitCond polls cond up to 300ms; fails the test on timeout.
func waitCond(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(1 * time.Millisecond)
	}
	t.Fatalf("waitCond timeout: %s", msg)
}

// --- acceptance criteria tests ----------------------------------------

func TestMatcher_LeafFiresAndResets(t *testing.T) {
	var fired []string
	var mu sync.Mutex
	cmd := recordingCmd("cursor.down", &fired, &mu, nil)
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeNormal, types.QUERY_EDITOR, []Key{keyOf('j')}, cmd},
	})
	m := shortMatcher(t, ts, types.QUERY_EDITOR, types.ModeNormal)

	res, err := m.Dispatch(types.QUERY_EDITOR, keyOf('j'))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if res != Dispatched {
		t.Errorf("res = %v, want Dispatched", res)
	}
	mu.Lock()
	got := append([]string(nil), fired...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "cursor.down" {
		t.Errorf("fired = %v, want [cursor.down]", got)
	}
	if m.IsPartial() {
		t.Errorf("IsPartial = true after dispatched leaf; want false")
	}
}

func TestMatcher_InteriorReturnsPending(t *testing.T) {
	var fired []string
	var mu sync.Mutex
	cmd := recordingCmd("buffer.top", &fired, &mu, nil)
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeNormal, types.QUERY_EDITOR, []Key{keyOf('g'), keyOf('g')}, cmd},
	})
	m := shortMatcher(t, ts, types.QUERY_EDITOR, types.ModeNormal)

	res, err := m.Dispatch(types.QUERY_EDITOR, keyOf('g'))
	if err != nil {
		t.Fatalf("Dispatch g: %v", err)
	}
	if res != Pending {
		t.Errorf("res = %v, want Pending", res)
	}
	if !m.IsPartial() {
		t.Errorf("IsPartial after partial g = false, want true")
	}
	mu.Lock()
	gotLen := len(fired)
	mu.Unlock()
	if gotLen != 0 {
		t.Errorf("handler fired during partial; got %d", gotLen)
	}
}

func TestMatcher_AmbiguousLeafTimerFires(t *testing.T) {
	var fired []string
	var mu sync.Mutex
	gTop := recordingCmd("line.top", &fired, &mu, nil)
	ggTop := recordingCmd("buffer.top", &fired, &mu, nil)
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeNormal, types.QUERY_EDITOR, []Key{keyOf('g')}, gTop},
		{types.ModeNormal, types.QUERY_EDITOR, []Key{keyOf('g'), keyOf('g')}, ggTop},
	})
	m := shortMatcher(t, ts, types.QUERY_EDITOR, types.ModeNormal)

	res, err := m.Dispatch(types.QUERY_EDITOR, keyOf('g'))
	if err != nil || res != Pending {
		t.Fatalf("Dispatch g: res=%v err=%v", res, err)
	}
	waitCond(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(fired) == 1 && fired[0] == "line.top"
	}, "ambiguous leaf timer should fire line.top after tlen")
	if m.IsPartial() {
		t.Errorf("IsPartial after timer fire = true, want false")
	}
}

func TestMatcher_AmbiguousLeafSecondKeyFiresLongerLeaf(t *testing.T) {
	var fired []string
	var mu sync.Mutex
	gTop := recordingCmd("line.top", &fired, &mu, nil)
	ggTop := recordingCmd("buffer.top", &fired, &mu, nil)
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeNormal, types.QUERY_EDITOR, []Key{keyOf('g')}, gTop},
		{types.ModeNormal, types.QUERY_EDITOR, []Key{keyOf('g'), keyOf('g')}, ggTop},
	})
	m := shortMatcher(t, ts, types.QUERY_EDITOR, types.ModeNormal)

	if _, err := m.Dispatch(types.QUERY_EDITOR, keyOf('g')); err != nil {
		t.Fatalf("Dispatch g(1): %v", err)
	}
	res, err := m.Dispatch(types.QUERY_EDITOR, keyOf('g'))
	if err != nil {
		t.Fatalf("Dispatch g(2): %v", err)
	}
	if res != Dispatched {
		t.Errorf("res = %v, want Dispatched", res)
	}
	mu.Lock()
	got := append([]string(nil), fired...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "buffer.top" {
		t.Errorf("fired = %v, want [buffer.top]", got)
	}
}

func TestMatcher_CancelClearsPendingAndTimer(t *testing.T) {
	var fired []string
	var mu sync.Mutex
	gTop := recordingCmd("line.top", &fired, &mu, nil)
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeNormal, types.QUERY_EDITOR, []Key{keyOf('g')}, gTop},
		{types.ModeNormal, types.QUERY_EDITOR, []Key{keyOf('g'), keyOf('g')},
			recordingCmd("buffer.top", &fired, &mu, nil)},
	})
	m := shortMatcher(t, ts, types.QUERY_EDITOR, types.ModeNormal)

	if _, err := m.Dispatch(types.QUERY_EDITOR, keyOf('g')); err != nil {
		t.Fatalf("Dispatch g: %v", err)
	}
	if !m.IsPartial() {
		t.Fatalf("precondition: IsPartial should be true")
	}
	m.Cancel()
	if m.IsPartial() {
		t.Errorf("IsPartial after Cancel = true, want false")
	}
	// Wait past the timer; nothing should have fired.
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	got := len(fired)
	mu.Unlock()
	if got != 0 {
		t.Errorf("handler fired after Cancel; got %d", got)
	}
}

func TestMatcher_SwapTrieSetCancelsPending(t *testing.T) {
	var fired []string
	var mu sync.Mutex
	leaderT := recordingCmd("test.partial", &fired, &mu, nil)
	ts1 := buildTrieSet(t, []trieEntry{
		{types.ModeNormal, types.QUERY_EDITOR, []Key{keyOf(' '), keyOf('t')}, leaderT},
	})
	ts2 := buildTrieSet(t, nil)
	m := shortMatcher(t, ts1, types.QUERY_EDITOR, types.ModeNormal)

	if _, err := m.Dispatch(types.QUERY_EDITOR, keyOf(' ')); err != nil {
		t.Fatalf("Dispatch space: %v", err)
	}
	if !m.IsPartial() {
		t.Fatalf("precondition: IsPartial should be true")
	}
	m.SwapTrieSet(ts2)
	if m.IsPartial() {
		t.Errorf("IsPartial after SwapTrieSet = true, want false")
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	got := len(fired)
	mu.Unlock()
	if got != 0 {
		t.Errorf("handler fired across swap; got %d", got)
	}
}

func TestMatcher_ScopeToGlobalFallThroughOneKey(t *testing.T) {
	var fired []string
	var mu sync.Mutex
	scopeLeader := recordingCmd("scope.partial", &fired, &mu, nil)
	globalJ := recordingCmd("cursor.down", &fired, &mu, nil)
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeNormal, types.QUERY_EDITOR, []Key{keyOf(' '), keyOf('t')}, scopeLeader},
		{types.ModeNormal, types.GLOBAL, []Key{keyOf('j')}, globalJ},
	})
	m := shortMatcher(t, ts, types.QUERY_EDITOR, types.ModeNormal)

	if _, err := m.Dispatch(types.QUERY_EDITOR, keyOf(' ')); err != nil {
		t.Fatalf("Dispatch leader: %v", err)
	}
	res, err := m.Dispatch(types.QUERY_EDITOR, keyOf('j'))
	if err != nil {
		t.Fatalf("Dispatch j: %v", err)
	}
	if res != Dispatched {
		t.Errorf("res = %v, want Dispatched", res)
	}
	mu.Lock()
	got := append([]string(nil), fired...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "cursor.down" {
		t.Errorf("fired = %v, want [cursor.down]", got)
	}
	if m.IsPartial() {
		t.Errorf("IsPartial after fallthrough = true, want false")
	}
}

func TestMatcher_CountCollectedAndPassedToHandler(t *testing.T) {
	var gotCtx commands.ExecCtx
	var mu sync.Mutex
	cmd := recordingCmdWithCtx("cursor.down", &gotCtx, &mu)
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeNormal, types.QUERY_EDITOR, []Key{keyOf('j')}, cmd},
	})
	m := shortMatcher(t, ts, types.QUERY_EDITOR, types.ModeNormal)

	for _, k := range []Key{keyOf('5'), keyOf('0'), keyOf('j')} {
		if _, err := m.Dispatch(types.QUERY_EDITOR, k); err != nil {
			t.Fatalf("Dispatch %v: %v", k, err)
		}
	}
	mu.Lock()
	count := gotCtx.Count
	mu.Unlock()
	if count != 50 {
		t.Errorf("Count = %d, want 50", count)
	}
	if m.IsPartial() {
		t.Errorf("IsPartial after dispatched 50j = true, want false")
	}
}

func TestMatcher_CountDroppedWhenSuffixUnbound(t *testing.T) {
	ts := buildTrieSet(t, nil) // empty trie — every key falls through
	m := shortMatcher(t, ts, types.QUERY_EDITOR, types.ModeNormal)

	if res, _ := m.Dispatch(types.QUERY_EDITOR, keyOf('5')); res != Pending {
		t.Fatalf("Dispatch 5: res=%v, want Pending", res)
	}
	res, err := m.Dispatch(types.QUERY_EDITOR, keyOf('z'))
	if err != nil {
		t.Fatalf("Dispatch z: %v", err)
	}
	if res != FellThrough {
		t.Errorf("res = %v, want FellThrough", res)
	}
	if m.IsPartial() {
		t.Errorf("IsPartial after dropped count = true, want false")
	}
}

func TestMatcher_RegisterPrefixSetsRegister(t *testing.T) {
	var gotCtx commands.ExecCtx
	var mu sync.Mutex
	yy := recordingCmdWithCtx("line.yank", &gotCtx, &mu)
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeNormal, types.QUERY_EDITOR, []Key{keyOf('y'), keyOf('y')}, yy},
	})
	m := shortMatcher(t, ts, types.QUERY_EDITOR, types.ModeNormal)

	for _, k := range []Key{keyOf('"'), keyOf('a'), keyOf('y'), keyOf('y')} {
		if _, err := m.Dispatch(types.QUERY_EDITOR, k); err != nil {
			t.Fatalf("Dispatch %v: %v", k, err)
		}
	}
	mu.Lock()
	reg := gotCtx.Register
	mu.Unlock()
	if reg != 'a' {
		t.Errorf("Register = %q, want 'a'", reg)
	}
}

func TestMatcher_LeaderDigitRejected(t *testing.T) {
	_, err := NewMatcher(nil, MatcherConfig{Leader: '5'})
	if err == nil {
		t.Fatalf("NewMatcher(leader='5') err = nil, want error")
	}
}

func TestMatcher_IsPartialReportsPending(t *testing.T) {
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeNormal, types.QUERY_EDITOR, []Key{keyOf('g'), keyOf('g')},
			fixtureCmd("buffer.top")},
	})
	m := shortMatcher(t, ts, types.QUERY_EDITOR, types.ModeNormal)
	if m.IsPartial() {
		t.Fatalf("IsPartial on fresh Matcher = true")
	}
	if _, err := m.Dispatch(types.QUERY_EDITOR, keyOf('g')); err != nil {
		t.Fatalf("Dispatch g: %v", err)
	}
	if !m.IsPartial() {
		t.Errorf("IsPartial after partial g = false, want true")
	}
}

func TestMatcher_EmptyTrieSetFellThrough(t *testing.T) {
	m := shortMatcher(t, &TrieSet{}, types.QUERY_EDITOR, types.ModeNormal)
	res, err := m.Dispatch(types.QUERY_EDITOR, keyOf('j'))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if res != FellThrough {
		t.Errorf("res on empty TrieSet = %v, want FellThrough", res)
	}
}

func TestMatcher_EscUsesTtimeoutlen(t *testing.T) {
	var fired []string
	var mu sync.Mutex
	escLeaf := recordingCmd("esc.action", &fired, &mu, nil)
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeNormal, types.QUERY_EDITOR, []Key{specialKey(KeyEsc)}, escLeaf},
		{types.ModeNormal, types.QUERY_EDITOR, []Key{specialKey(KeyEsc), keyOf('a')},
			fixtureCmd("esc.a")},
	})
	store := NewModeStore()
	store.Set(types.QUERY_EDITOR, types.ModeNormal)
	m, err := NewMatcher(ts, MatcherConfig{
		Modes:       store,
		TimeoutLen:  500 * time.Millisecond,
		TtimeoutLen: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	res, err := m.Dispatch(types.QUERY_EDITOR, specialKey(KeyEsc))
	if err != nil {
		t.Fatalf("Dispatch esc: %v", err)
	}
	if res != Pending {
		t.Fatalf("res = %v, want Pending", res)
	}
	// Wait shorter than tlen (500ms) but longer than ttlen (5ms).
	waitCond(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(fired) == 1
	}, "esc leaf should fire within ttlen, not wait for tlen")
}

func TestMatcher_PassthroughInsertMode(t *testing.T) {
	ts := buildTrieSet(t, nil)
	store := NewModeStore()
	store.Set(types.QUERY_EDITOR, types.ModeInsert)
	m, err := NewMatcher(ts, MatcherConfig{
		Modes:      store,
		TimeoutLen: 30 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	res, err := m.Dispatch(types.QUERY_EDITOR, keyOf('x'))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if res != Passthrough {
		t.Errorf("res = %v, want Passthrough", res)
	}
	// Digit in Insert mode is also Passthrough (no count collection).
	res, _ = m.Dispatch(types.QUERY_EDITOR, keyOf('5'))
	if res != Passthrough {
		t.Errorf("digit in insert: res = %v, want Passthrough", res)
	}
}

func TestMatcher_InsertModeBindingStillMatches(t *testing.T) {
	var fired []string
	var mu sync.Mutex
	jk := recordingCmd("mode.normal_from_insert", &fired, &mu, nil)
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeInsert, types.QUERY_EDITOR, []Key{keyOf('j'), keyOf('k')}, jk},
	})
	store := NewModeStore()
	store.Set(types.QUERY_EDITOR, types.ModeInsert)
	m, err := NewMatcher(ts, MatcherConfig{
		Modes:       store,
		TimeoutLen:  500 * time.Millisecond,
		TtimeoutLen: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}

	res, err := m.Dispatch(types.QUERY_EDITOR, keyOf('j'))
	if err != nil {
		t.Fatalf("Dispatch j: %v", err)
	}
	if res != Pending {
		t.Errorf("Dispatch j: res = %v, want Pending", res)
	}
	res, err = m.Dispatch(types.QUERY_EDITOR, keyOf('k'))
	if err != nil {
		t.Fatalf("Dispatch k: %v", err)
	}
	if res != Dispatched {
		t.Errorf("Dispatch k: res = %v, want Dispatched", res)
	}
	mu.Lock()
	got := append([]string(nil), fired...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "mode.normal_from_insert" {
		t.Errorf("fired = %v, want [mode.normal_from_insert]", got)
	}
}

func TestMatcher_InsertPendingFlushOnTimeout(t *testing.T) {
	jk := fixtureCmd("mode.normal_from_insert")
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeInsert, types.QUERY_EDITOR, []Key{keyOf('j'), keyOf('k')}, jk},
	})
	store := NewModeStore()
	store.Set(types.QUERY_EDITOR, types.ModeInsert)
	m, err := NewMatcher(ts, MatcherConfig{
		Modes:       store,
		TimeoutLen:  15 * time.Millisecond,
		TtimeoutLen: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}

	type flush struct {
		scope types.ContextKey
		runes []rune
	}
	flushes := make(chan flush, 4)
	m.OnInsertPendingFlush(func(scope types.ContextKey, runes []rune) {
		flushes <- flush{scope: scope, runes: append([]rune(nil), runes...)}
	})

	if _, err := m.Dispatch(types.QUERY_EDITOR, keyOf('j')); err != nil {
		t.Fatalf("Dispatch j: %v", err)
	}

	select {
	case f := <-flushes:
		if f.scope != types.QUERY_EDITOR {
			t.Errorf("flush scope = %v, want QUERY_EDITOR", f.scope)
		}
		if len(f.runes) != 1 || f.runes[0] != 'j' {
			t.Errorf("flush runes = %v, want [j]", f.runes)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("InsertPendingFlush not invoked on timer fire")
	}
	if m.IsPartial() {
		t.Errorf("IsPartial after flush = true, want false")
	}
}

func TestMatcher_InsertPendingFlushNotInvokedOnLeafMatch(t *testing.T) {
	jk := fixtureCmd("mode.normal_from_insert")
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeInsert, types.QUERY_EDITOR, []Key{keyOf('j'), keyOf('k')}, jk},
	})
	store := NewModeStore()
	store.Set(types.QUERY_EDITOR, types.ModeInsert)
	m, err := NewMatcher(ts, MatcherConfig{
		Modes:       store,
		TimeoutLen:  30 * time.Millisecond,
		TtimeoutLen: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}

	var flushCalled atomic.Int32
	m.OnInsertPendingFlush(func(scope types.ContextKey, runes []rune) {
		flushCalled.Add(1)
	})

	if _, err := m.Dispatch(types.QUERY_EDITOR, keyOf('j')); err != nil {
		t.Fatalf("Dispatch j: %v", err)
	}
	if _, err := m.Dispatch(types.QUERY_EDITOR, keyOf('k')); err != nil {
		t.Fatalf("Dispatch k: %v", err)
	}
	time.Sleep(60 * time.Millisecond) // past the tlen
	if got := flushCalled.Load(); got != 0 {
		t.Errorf("flush called %d times after leaf match; want 0", got)
	}
}

// handlerReenter is the reentrance test: a handler that calls
// matcher.Cancel synchronously must not deadlock.
func TestMatcher_HandlerReentranceNoDeadlock(t *testing.T) {
	var m *Matcher
	done := make(chan struct{})
	cmd := &commands.Command{
		ID: "test.reenter",
		Handler: func(ctx commands.ExecCtx) error {
			// Re-enter the Matcher synchronously. With m.mu released
			// before Handler invocation, this must NOT deadlock.
			m.Cancel()
			close(done)
			return nil
		},
	}
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeNormal, types.QUERY_EDITOR, []Key{keyOf('z')}, cmd},
	})
	m = shortMatcher(t, ts, types.QUERY_EDITOR, types.ModeNormal)

	finished := make(chan struct{})
	go func() {
		_, _ = m.Dispatch(types.QUERY_EDITOR, keyOf('z'))
		close(finished)
	}()

	select {
	case <-finished:
		// OK
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("Dispatch did not return; handler reentrance deadlocked")
	}
	select {
	case <-done:
	case <-time.After(50 * time.Millisecond):
		t.Fatalf("handler did not complete")
	}
}

func TestMatcher_HandlerErrorReturnedStateReset(t *testing.T) {
	sentinel := errors.New("boom")
	cmd := &commands.Command{
		ID: "test.err",
		Handler: func(ctx commands.ExecCtx) error {
			return sentinel
		},
	}
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeNormal, types.QUERY_EDITOR, []Key{keyOf('z')}, cmd},
	})
	m := shortMatcher(t, ts, types.QUERY_EDITOR, types.ModeNormal)

	res, err := m.Dispatch(types.QUERY_EDITOR, keyOf('z'))
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want %v", err, sentinel)
	}
	if res != Dispatched {
		t.Errorf("res = %v, want Dispatched", res)
	}
	if m.IsPartial() {
		t.Errorf("IsPartial after handler error = true, want false")
	}
}

func TestMatcher_HandlerPanicRecovered(t *testing.T) {
	cmd := &commands.Command{
		ID: "test.panic",
		Handler: func(ctx commands.ExecCtx) error {
			panic("kaboom")
		},
	}
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeNormal, types.QUERY_EDITOR, []Key{keyOf('z')}, cmd},
	})
	m := shortMatcher(t, ts, types.QUERY_EDITOR, types.ModeNormal)

	res, err := m.Dispatch(types.QUERY_EDITOR, keyOf('z'))
	if err == nil {
		t.Fatalf("err = nil after panic; want non-nil")
	}
	if res != Dispatched {
		t.Errorf("res = %v, want Dispatched", res)
	}
	if m.IsPartial() {
		t.Errorf("IsPartial after panic recovery = true, want false")
	}
}

func TestMatcher_CountOverflowClamped(t *testing.T) {
	var gotCtx commands.ExecCtx
	var mu sync.Mutex
	cmd := recordingCmdWithCtx("cursor.down", &gotCtx, &mu)
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeNormal, types.QUERY_EDITOR, []Key{keyOf('j')}, cmd},
	})
	m := shortMatcher(t, ts, types.QUERY_EDITOR, types.ModeNormal)

	// 50 nines (overflow if unclamped).
	for i := 0; i < 50; i++ {
		if _, err := m.Dispatch(types.QUERY_EDITOR, keyOf('9')); err != nil {
			t.Fatalf("Dispatch 9: %v", err)
		}
	}
	if _, err := m.Dispatch(types.QUERY_EDITOR, keyOf('j')); err != nil {
		t.Fatalf("Dispatch j: %v", err)
	}
	mu.Lock()
	c := gotCtx.Count
	mu.Unlock()
	if c <= 0 {
		t.Errorf("Count = %d; expected clamped positive value", c)
	}
	if c > countMax {
		t.Errorf("Count = %d > countMax = %d", c, countMax)
	}
}

func TestMatcher_AfterFuncStaleFireSuppressed(t *testing.T) {
	var fired []string
	var mu sync.Mutex
	gTop := recordingCmd("line.top", &fired, &mu, nil)
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeNormal, types.QUERY_EDITOR, []Key{keyOf('g')}, gTop},
		{types.ModeNormal, types.QUERY_EDITOR, []Key{keyOf('g'), keyOf('g')},
			fixtureCmd("buffer.top")},
	})
	// Long enough that we control when the timer would fire.
	store := NewModeStore()
	store.Set(types.QUERY_EDITOR, types.ModeNormal)
	m, err := NewMatcher(ts, MatcherConfig{
		Modes:      store,
		TimeoutLen: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}

	if _, err := m.Dispatch(types.QUERY_EDITOR, keyOf('g')); err != nil {
		t.Fatalf("Dispatch g: %v", err)
	}
	// Cancel immediately — the AfterFunc will still race the cancel,
	// but the captured id check inside onTimerFire must suppress it.
	m.Cancel()

	// Sleep > timeoutlen and confirm no fire.
	time.Sleep(40 * time.Millisecond)
	mu.Lock()
	got := len(fired)
	mu.Unlock()
	if got != 0 {
		t.Errorf("stale timer fired %d times; want 0", got)
	}
}
