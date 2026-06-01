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
		{
			types.ModeNormal, types.QUERY_EDITOR,
			[]Key{keyOf('g'), keyOf('g')},
			recordingCmd("buffer.top", &fired, &mu, nil),
		},
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

func TestMatcher_ScopeToGlobalFallThroughChord(t *testing.T) {
	// Regression: <leader>p is a GLOBAL chord, but from query_editor
	// (which has <leader>t, <leader>e, etc.) the scope trie consumes
	// <leader> as a prefix. When the second key (p) doesn't match any
	// scope binding, the fallback must try [<space>,p] against global,
	// not just [p] alone.
	var fired []string
	var mu sync.Mutex
	scopeCmd := recordingCmd("scope.thing", &fired, &mu, nil)
	globalChord := recordingCmd("session.search_path", &fired, &mu, nil)
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeNormal, types.QUERY_EDITOR, []Key{keyOf(' '), keyOf('t')}, scopeCmd},
		{types.ModeNormal, types.GLOBAL, []Key{keyOf(' '), keyOf('p')}, globalChord},
	})
	m := shortMatcher(t, ts, types.QUERY_EDITOR, types.ModeNormal)

	if _, err := m.Dispatch(types.QUERY_EDITOR, keyOf(' ')); err != nil {
		t.Fatalf("Dispatch leader: %v", err)
	}
	res, err := m.Dispatch(types.QUERY_EDITOR, keyOf('p'))
	if err != nil {
		t.Fatalf("Dispatch p: %v", err)
	}
	if res != Dispatched {
		t.Errorf("res = %v, want Dispatched", res)
	}
	mu.Lock()
	got := append([]string(nil), fired...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "session.search_path" {
		t.Errorf("fired = %v, want [session.search_path]", got)
	}
	if m.IsPartial() {
		t.Errorf("IsPartial after global chord = true, want false")
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
		{
			types.ModeNormal, types.QUERY_EDITOR,
			[]Key{keyOf('g'), keyOf('g')},
			fixtureCmd("buffer.top"),
		},
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
		{
			types.ModeNormal, types.QUERY_EDITOR,
			[]Key{specialKey(KeyEsc), keyOf('a')},
			fixtureCmd("esc.a"),
		},
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
	m.OnInsertPendingFlush(types.QUERY_EDITOR, func(scope types.ContextKey, runes []rune) {
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
	m.OnInsertPendingFlush(types.QUERY_EDITOR, func(scope types.ContextKey, runes []rune) {
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

// TestMatcher_InsertPendingFlushOnBrokenSequence covers the `jk` case
// where `j` is buffered (prefix of jk) and the NEXT key is not `k`: the
// `j` must flush synchronously from Dispatch (before the breaking key is
// passed through), NOT wait for the chord timeout. Otherwise typing
// "join" would drop the leading `j`.
func TestMatcher_InsertPendingFlushOnBrokenSequence(t *testing.T) {
	jk := fixtureCmd("mode.normal_from_insert")
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeInsert, types.QUERY_EDITOR, []Key{keyOf('j'), keyOf('k')}, jk},
	})
	store := NewModeStore()
	store.Set(types.QUERY_EDITOR, types.ModeInsert)
	m, err := NewMatcher(ts, MatcherConfig{
		Modes: store,
		// Long timeout: the flush under test must come from the broken
		// sequence, not the timer.
		TimeoutLen: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}

	var flushed [][]rune
	m.OnInsertPendingFlush(types.QUERY_EDITOR, func(_ types.ContextKey, runes []rune) {
		flushed = append(flushed, append([]rune(nil), runes...))
	})

	if res, _ := m.Dispatch(types.QUERY_EDITOR, keyOf('j')); res != Pending {
		t.Fatalf("Dispatch j = %v, want Pending", res)
	}
	res, _ := m.Dispatch(types.QUERY_EDITOR, keyOf('o'))
	if res != Passthrough {
		t.Fatalf("Dispatch o = %v, want Passthrough", res)
	}
	if len(flushed) != 1 || len(flushed[0]) != 1 || flushed[0][0] != 'j' {
		t.Fatalf("flushed = %v, want [[j]]", flushed)
	}
	if m.IsPartial() {
		t.Errorf("IsPartial after broken sequence = true, want false")
	}
}

// TestMatcher_InsertPendingFlushPerScope proves the flush registry is
// keyed by scope: a callback registered for an unrelated scope LAST must
// not shadow QUERY_EDITOR's flush (the old single-slot design did).
func TestMatcher_InsertPendingFlushPerScope(t *testing.T) {
	jk := fixtureCmd("mode.normal_from_insert")
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeInsert, types.QUERY_EDITOR, []Key{keyOf('j'), keyOf('k')}, jk},
	})
	store := NewModeStore()
	store.Set(types.QUERY_EDITOR, types.ModeInsert)
	m, err := NewMatcher(ts, MatcherConfig{Modes: store, TimeoutLen: 15 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}

	qe := make(chan []rune, 1)
	other := make(chan struct{}, 1)
	m.OnInsertPendingFlush(types.QUERY_EDITOR, func(_ types.ContextKey, r []rune) {
		qe <- append([]rune(nil), r...)
	})
	// Registered last: under the old last-writer-wins slot this would have
	// captured the only callback and the QUERY_EDITOR flush below would be
	// dropped.
	m.OnInsertPendingFlush(types.COMMAND_LINE, func(types.ContextKey, []rune) {
		other <- struct{}{}
	})

	if _, err := m.Dispatch(types.QUERY_EDITOR, keyOf('j')); err != nil {
		t.Fatalf("Dispatch j: %v", err)
	}
	select {
	case r := <-qe:
		if len(r) != 1 || r[0] != 'j' {
			t.Fatalf("QUERY_EDITOR flush = %v, want [j]", r)
		}
	case <-other:
		t.Fatal("COMMAND_LINE callback fired for a QUERY_EDITOR pending")
	case <-time.After(200 * time.Millisecond):
		t.Fatal("QUERY_EDITOR flush not invoked")
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
	// The central error boundary (dbsavvy-9v1.4) swallows handler errors so
	// gocui's MainLoop never sees them; the sanitized toast is covered by
	// TestMatcher_HandlerError_ToastAndSwallowed. This test's purpose is the
	// post-error state reset asserted below.
	if err != nil {
		t.Errorf("err = %v, want nil (swallowed by boundary)", err)
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
		{
			types.ModeNormal, types.QUERY_EDITOR,
			[]Key{keyOf('g'), keyOf('g')},
			fixtureCmd("buffer.top"),
		},
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

// --- dbsavvy-tro.6: editor-safe Special-key Passthrough --------------
//
// These tests pin the contract that the Matcher's Passthrough gate
// (matcher.go: Step 3) returns Passthrough — NOT FellThrough — for
// non-printable editor keys (Backspace, Delete, arrows, Home, End) in
// ModeInsert / ModeCommand. The master editor's applyResult routes
// Passthrough to gocui.DefaultEditor, which handles BackSpaceChar /
// DeleteChar / cursor moves natively. Without this gate the keys are
// silently dropped because the view's Editor IS the master editor —
// gocui never reaches DefaultEditor on its own.

// TestMatcher_BackspaceInModeCommand_ReturnsPassthrough is the
// canonical regression: the original walkthrough bug was Backspace
// being dropped in the COMMAND_LINE. Empty trieset so the matcher
// reaches Step 3 (no binding, no partial), where the widened gate
// must fire.
func TestMatcher_BackspaceInModeCommand_ReturnsPassthrough(t *testing.T) {
	ts := buildTrieSet(t, nil)
	m := shortMatcher(t, ts, types.COMMAND_LINE, types.ModeCommand)

	res, err := m.Dispatch(types.COMMAND_LINE, specialKey(KeyBs))
	if err != nil {
		t.Fatalf("Dispatch <bs>: %v", err)
	}
	if res != Passthrough {
		t.Errorf("res = %v, want Passthrough (Backspace must reach DefaultEditor)", res)
	}
}

// TestMatcher_EditorSafeSpecials_ModeCommand asserts every key in the
// editor-safe Special set returns Passthrough under ModeCommand.
// Drives them through fresh matchers so leftover pending state from
// one key cannot mask a regression on the next.
func TestMatcher_EditorSafeSpecials_ModeCommand(t *testing.T) {
	cases := []struct {
		name string
		sp   SpecialKey
	}{
		{"Backspace", KeyBs},
		{"Delete", KeyDel},
		{"ArrowLeft", KeyLeft},
		{"ArrowRight", KeyRight},
		{"ArrowUp", KeyUp},
		{"ArrowDown", KeyDown},
		{"Home", KeyHome},
		{"End", KeyEnd},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := buildTrieSet(t, nil)
			m := shortMatcher(t, ts, types.COMMAND_LINE, types.ModeCommand)
			res, err := m.Dispatch(types.COMMAND_LINE, specialKey(tc.sp))
			if err != nil {
				t.Fatalf("Dispatch %s: %v", tc.name, err)
			}
			if res != Passthrough {
				t.Errorf("%s in ModeCommand: res = %v, want Passthrough", tc.name, res)
			}
		})
	}
}

// TestMatcher_EditorSafeSpecials_ModeInsert mirrors the ModeCommand
// suite under ModeInsert; the gate widens both modes symmetrically.
func TestMatcher_EditorSafeSpecials_ModeInsert(t *testing.T) {
	cases := []struct {
		name string
		sp   SpecialKey
	}{
		{"Backspace", KeyBs},
		{"Delete", KeyDel},
		{"ArrowLeft", KeyLeft},
		{"ArrowRight", KeyRight},
		{"ArrowUp", KeyUp},
		{"ArrowDown", KeyDown},
		{"Home", KeyHome},
		{"End", KeyEnd},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := buildTrieSet(t, nil)
			m := shortMatcher(t, ts, types.QUERY_EDITOR, types.ModeInsert)
			res, err := m.Dispatch(types.QUERY_EDITOR, specialKey(tc.sp))
			if err != nil {
				t.Fatalf("Dispatch %s: %v", tc.name, err)
			}
			if res != Passthrough {
				t.Errorf("%s in ModeInsert: res = %v, want Passthrough", tc.name, res)
			}
		})
	}
}

// TestMatcher_NonEditorSpecials_StillFellThrough is the negative case
// guarding against an over-broad helper: F1 / PgUp / PgDn / Insert /
// Tab / Enter / Esc are NOT editor-safe (DefaultEditor either has no
// useful behaviour or those keys have higher-level meaning) and must
// continue to FellThrough so they can be picked up by Editor-external
// keybindings.
func TestMatcher_NonEditorSpecials_StillFellThrough(t *testing.T) {
	cases := []struct {
		name string
		sp   SpecialKey
	}{
		{"F1", KeyF1},
		{"F12", KeyF12},
		{"PgUp", KeyPgUp},
		{"PgDn", KeyPgDn},
		{"Insert", KeyIns},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := buildTrieSet(t, nil)
			m := shortMatcher(t, ts, types.COMMAND_LINE, types.ModeCommand)
			res, err := m.Dispatch(types.COMMAND_LINE, specialKey(tc.sp))
			if err != nil {
				t.Fatalf("Dispatch %s: %v", tc.name, err)
			}
			if res != FellThrough {
				t.Errorf("%s in ModeCommand: res = %v, want FellThrough (not editor-safe)", tc.name, res)
			}
		})
	}
}

// TestMatcher_BackspaceInModeNormal_FellThrough guards the mode side
// of the gate: outside ModeInsert / ModeCommand, even editor-safe
// Special keys must FellThrough so Normal-mode bindings (or
// higher-level handlers) can claim them.
func TestMatcher_BackspaceInModeNormal_FellThrough(t *testing.T) {
	ts := buildTrieSet(t, nil)
	m := shortMatcher(t, ts, types.QUERY_EDITOR, types.ModeNormal)

	res, err := m.Dispatch(types.QUERY_EDITOR, specialKey(KeyBs))
	if err != nil {
		t.Fatalf("Dispatch <bs>: %v", err)
	}
	if res != FellThrough {
		t.Errorf("res = %v, want FellThrough (Backspace must NOT passthrough in Normal mode)", res)
	}
}

// TestMatcher_BackspaceWithModifier_FellThrough pins the modifier
// exclusion: <c-bs> is NOT editor-safe (it has no DefaultEditor
// handler in our setup and should remain available for user
// bindings). The gate's Mod != 0 check enforces this.
func TestMatcher_BackspaceWithModifier_FellThrough(t *testing.T) {
	ts := buildTrieSet(t, nil)
	m := shortMatcher(t, ts, types.COMMAND_LINE, types.ModeCommand)

	res, err := m.Dispatch(types.COMMAND_LINE, Key{Special: KeyBs, Mod: ModCtrl})
	if err != nil {
		t.Fatalf("Dispatch <c-bs>: %v", err)
	}
	if res != FellThrough {
		t.Errorf("<c-bs> in ModeCommand: res = %v, want FellThrough (modifier excluded)", res)
	}
}

// TestMatcher_PrintableStillPassthrough_Regression guards the
// existing printable-rune behaviour from breaking when the gate is
// widened. Mirrors TestMatcher_PassthroughInsertMode but in
// ModeCommand for parity with the new code.
func TestMatcher_PrintableStillPassthrough_Regression(t *testing.T) {
	ts := buildTrieSet(t, nil)
	m := shortMatcher(t, ts, types.COMMAND_LINE, types.ModeCommand)

	res, err := m.Dispatch(types.COMMAND_LINE, keyOf('a'))
	if err != nil {
		t.Fatalf("Dispatch a: %v", err)
	}
	if res != Passthrough {
		t.Errorf("'a' in ModeCommand: res = %v, want Passthrough (printable regression)", res)
	}
}

// TestMatcher_BackspaceWithBinding_Dispatched proves bindings still
// win over the editor-safe Passthrough fallback: if the user binds
// <bs> to a command, the trie lookup at Step 1/2 fires before Step 3.
// The widened gate is strictly a no-binding fallback.
func TestMatcher_BackspaceWithBinding_Dispatched(t *testing.T) {
	var fired []string
	var mu sync.Mutex
	bsCmd := recordingCmd("test.bs", &fired, &mu, nil)
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeCommand, types.COMMAND_LINE, []Key{specialKey(KeyBs)}, bsCmd},
	})
	m := shortMatcher(t, ts, types.COMMAND_LINE, types.ModeCommand)

	res, err := m.Dispatch(types.COMMAND_LINE, specialKey(KeyBs))
	if err != nil {
		t.Fatalf("Dispatch <bs>: %v", err)
	}
	if res != Dispatched {
		t.Errorf("res = %v, want Dispatched (binding must win over passthrough)", res)
	}
	mu.Lock()
	got := append([]string(nil), fired...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "test.bs" {
		t.Errorf("fired = %v, want [test.bs]", got)
	}
}

// TestIsEditorSafeSpecial directly exercises the helper so a refactor
// renaming or inlining it still has a focused unit-level safety net.
func TestIsEditorSafeSpecial(t *testing.T) {
	wantTrue := []SpecialKey{KeyBs, KeyDel, KeyLeft, KeyRight, KeyUp, KeyDown, KeyHome, KeyEnd}
	for _, sp := range wantTrue {
		if !isEditorSafeSpecial(Key{Special: sp}) {
			t.Errorf("isEditorSafeSpecial(%v) = false, want true", sp)
		}
	}
	wantFalse := []Key{
		{Special: KeyF1},
		{Special: KeyPgUp},
		{Special: KeyPgDn},
		{Special: KeyIns},
		{Special: KeyEsc},
		{Special: KeyEnter},
		{Special: KeyTab},
		{Special: KeySpace},
		{Special: KeyNone},                // bare rune carrier
		{Code: 'a'},                       // plain rune
		{Special: KeyBs, Mod: ModCtrl},    // editor-safe key + modifier
		{Special: KeyLeft, Mod: ModShift}, // ditto
	}
	for _, k := range wantFalse {
		if isEditorSafeSpecial(k) {
			t.Errorf("isEditorSafeSpecial(%+v) = true, want false", k)
		}
	}
}

// --- dbsavvy-tro.5: which-key dismissal on unmatched continuation / esc --

// recordingNotifier is a minimal WhichKeyNotifier fake that counts
// ShowAfter / Hide calls. Safe for concurrent use.
type recordingNotifier struct {
	mu        sync.Mutex
	showCalls int
	hideCalls int
}

func (r *recordingNotifier) ShowAfter(_ time.Duration, _ types.ContextKey, _ []Key) {
	r.mu.Lock()
	r.showCalls++
	r.mu.Unlock()
}

func (r *recordingNotifier) Hide() {
	r.mu.Lock()
	r.hideCalls++
	r.mu.Unlock()
}

func (r *recordingNotifier) hides() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hideCalls
}

func (r *recordingNotifier) shows() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.showCalls
}

// matcherWithNotifier builds a Matcher wired to the supplied notifier
// with a non-zero WhichKeyDelay so notifyWhichKeyLocked actually calls
// ShowAfter.
func matcherWithNotifier(t *testing.T, ts *TrieSet, scope types.ContextKey, mode types.Mode, nf WhichKeyNotifier) *Matcher {
	t.Helper()
	store := NewModeStore()
	store.Set(scope, mode)
	m, err := NewMatcher(ts, MatcherConfig{
		Modes:         store,
		TimeoutLen:    30 * time.Millisecond,
		TtimeoutLen:   10 * time.Millisecond,
		WhichKeyDelay: 5 * time.Millisecond,
		WhichKey:      nf,
	})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	return m
}

// TestMatcher_DispatchUnmatchedAfterChordPrefix_HidesWhichKey covers the
// .5 regression: Space-prefix pending → unknown continuation key →
// popup must be hidden after dispatch (no waiting for auto-timeout).
func TestMatcher_DispatchUnmatchedAfterChordPrefix_HidesWhichKey(t *testing.T) {
	var fired []string
	var mu sync.Mutex
	cmd := recordingCmd("editor.write", &fired, &mu, nil)
	// Chord <space>w in QUERY_EDITOR scope; nothing else bound.
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeNormal, types.QUERY_EDITOR, []Key{specialKey(KeySpace), keyOf('w')}, cmd},
	})
	nf := &recordingNotifier{}
	m := matcherWithNotifier(t, ts, types.QUERY_EDITOR, types.ModeNormal, nf)

	// Enter the prefix.
	res, err := m.Dispatch(types.QUERY_EDITOR, specialKey(KeySpace))
	if err != nil {
		t.Fatalf("Dispatch <space>: %v", err)
	}
	if res != Pending {
		t.Fatalf("after <space>: res = %v, want Pending", res)
	}
	if !m.IsPartial() {
		t.Fatalf("IsPartial after <space> = false; want true")
	}
	if nf.shows() != 1 {
		t.Fatalf("ShowAfter calls after <space> = %d, want 1", nf.shows())
	}

	// Press an unmatched continuation key.
	res, err = m.Dispatch(types.QUERY_EDITOR, keyOf('z'))
	if err != nil {
		t.Fatalf("Dispatch z: %v", err)
	}
	if res != FellThrough {
		t.Fatalf("after <space>z: res = %v, want FellThrough", res)
	}
	if m.IsPartial() {
		t.Fatalf("IsPartial after fall-through = true; want false (state must reset)")
	}
	if got := nf.hides(); got != 1 {
		t.Errorf("Hide calls = %d, want exactly 1 after unmatched continuation", got)
	}
	mu.Lock()
	gotLen := len(fired)
	mu.Unlock()
	if gotLen != 0 {
		t.Errorf("handler fired during fall-through; got %d", gotLen)
	}
}

// TestMatcher_CancelEscAfterChordPrefix_HidesWhichKey is the <esc> path
// regression — Cancel() must hide the popup exactly once when a chord
// prefix is pending.
func TestMatcher_CancelEscAfterChordPrefix_HidesWhichKey(t *testing.T) {
	cmd := recordingCmd("editor.write", &[]string{}, &sync.Mutex{}, nil)
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeNormal, types.QUERY_EDITOR, []Key{specialKey(KeySpace), keyOf('w')}, cmd},
	})
	nf := &recordingNotifier{}
	m := matcherWithNotifier(t, ts, types.QUERY_EDITOR, types.ModeNormal, nf)

	if _, err := m.Dispatch(types.QUERY_EDITOR, specialKey(KeySpace)); err != nil {
		t.Fatalf("Dispatch <space>: %v", err)
	}
	if !m.IsPartial() {
		t.Fatalf("expected IsPartial after <space>")
	}
	if nf.shows() != 1 {
		t.Fatalf("ShowAfter calls = %d, want 1", nf.shows())
	}

	m.Cancel()

	if m.IsPartial() {
		t.Errorf("IsPartial after Cancel = true; want false")
	}
	if got := nf.hides(); got != 1 {
		t.Errorf("Hide calls after Cancel = %d, want exactly 1", got)
	}
}

// TestMatcher_DispatchMatchedAfterChordPrefix_DoesNotHideSpuriously
// makes sure the matched-continuation path keeps firing its leaf and
// only calls Hide once (handleLookup's own Hide on leaf fire) — the new
// FellThrough Hide branch must not double-fire on a successful match.
func TestMatcher_DispatchMatchedAfterChordPrefix_DoesNotHideSpuriously(t *testing.T) {
	var fired []string
	var mu sync.Mutex
	cmd := recordingCmd("editor.write", &fired, &mu, nil)
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeNormal, types.QUERY_EDITOR, []Key{specialKey(KeySpace), keyOf('w')}, cmd},
	})
	nf := &recordingNotifier{}
	m := matcherWithNotifier(t, ts, types.QUERY_EDITOR, types.ModeNormal, nf)

	if _, err := m.Dispatch(types.QUERY_EDITOR, specialKey(KeySpace)); err != nil {
		t.Fatalf("Dispatch <space>: %v", err)
	}
	res, err := m.Dispatch(types.QUERY_EDITOR, keyOf('w'))
	if err != nil {
		t.Fatalf("Dispatch w: %v", err)
	}
	if res != Dispatched {
		t.Fatalf("res = %v, want Dispatched", res)
	}
	mu.Lock()
	gotFired := append([]string(nil), fired...)
	mu.Unlock()
	if len(gotFired) != 1 || gotFired[0] != "editor.write" {
		t.Errorf("fired = %v, want [editor.write]", gotFired)
	}
	if got := nf.hides(); got != 1 {
		t.Errorf("Hide calls on matched continuation = %d, want exactly 1 (no double-hide)", got)
	}
}
