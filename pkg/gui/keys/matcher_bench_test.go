package keys

import (
	"os"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// benchMatcherInsert constructs a Matcher in ModeInsert with no bindings
// for printable runes, so every Dispatch follows the Passthrough fast
// path. The mode is set directly via ModeStore.Set — bench-only
// injection; the production Insert-entry transition has not shipped.
func benchMatcherInsert(tb testing.TB, ts *TrieSet) (*Matcher, types.ContextKey) {
	tb.Helper()
	scope := types.QUERY_EDITOR
	store := NewModeStore()
	store.Set(scope, types.ModeInsert)
	m, err := NewMatcher(ts, MatcherConfig{
		Modes:         store,
		TimeoutLen:    1 * time.Second,
		TtimeoutLen:   1 * time.Second,
		WhichKeyDelay: 300 * time.Millisecond,
	})
	if err != nil {
		tb.Fatalf("NewMatcher: %v", err)
	}
	return m, scope
}

// benchMatcherNormal constructs a Matcher in ModeNormal with the given
// TrieSet. Used for chord-idle / chord-partial benches.
func benchMatcherNormal(tb testing.TB, ts *TrieSet) (*Matcher, types.ContextKey) {
	tb.Helper()
	scope := types.QUERY_EDITOR
	store := NewModeStore()
	store.Set(scope, types.ModeNormal)
	m, err := NewMatcher(ts, MatcherConfig{
		Modes:         store,
		TimeoutLen:    1 * time.Second,
		TtimeoutLen:   1 * time.Second,
		WhichKeyDelay: 300 * time.Millisecond,
	})
	if err != nil {
		tb.Fatalf("NewMatcher: %v", err)
	}
	return m, scope
}

// idleTrieSet builds a small Normal-mode trie of ~50 single-key leaves
// using runes outside the keypress alphabet used by the bench, so every
// bench keypress falls through.
func idleTrieSet(tb testing.TB) *TrieSet {
	tb.Helper()
	entries := make([]trieEntry, 0, 50)
	for i := range 50 {
		r := rune('A' + i%26)
		entries = append(entries, trieEntry{
			mode:  types.ModeNormal,
			scope: types.QUERY_EDITOR,
			seq:   []Key{{Code: r}},
			cmd:   fixtureCmd("bench.idle"),
		})
	}
	return buildTrieSetTB(tb, entries)
}

// partialTrieSet builds a trie with a 2-key chord on (g, g) plus a
// handful of single-key leaves outside the bench alphabet. Every 5th
// bench key enters PARTIAL via 'g', then the next 'g' dispatches the
// leaf.
func partialTrieSet(tb testing.TB) *TrieSet {
	tb.Helper()
	entries := []trieEntry{
		{
			mode:  types.ModeNormal,
			scope: types.QUERY_EDITOR,
			seq:   []Key{{Code: 'g'}, {Code: 'g'}},
			cmd:   fixtureCmd("bench.gg"),
		},
	}
	return buildTrieSetTB(tb, entries)
}

// buildTrieSetTB is the testing.TB-flavoured twin of buildTrieSet (which
// takes *testing.T). Kept local to this file so benches can construct
// TrieSets without modifying matcher_test.go.
func buildTrieSetTB(tb testing.TB, entries []trieEntry) *TrieSet {
	tb.Helper()
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
			Origin:   "bench",
		}, e.cmd)
	}
	out := &TrieSet{tries: map[TrieSetKey]*ChordTrie{}}
	for k, b := range builders {
		trie, _ := b.Build()
		out.tries[k] = trie
	}
	return out
}

func BenchmarkMatcher_PasteInsertMode(b *testing.B) {
	ts := buildTrieSetTB(b, nil)
	m, scope := benchMatcherInsert(b, ts)
	const n = 4000
	keys := make([]Key, n)
	for i := range n {
		keys[i] = Key{Code: 'a' + rune(i%26)}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := range n {
			res, err := m.Dispatch(scope, keys[j])
			if err != nil {
				b.Fatalf("Dispatch err: %v", err)
			}
			if res != Passthrough {
				b.Fatalf("res=%v want Passthrough at j=%d", res, j)
			}
		}
	}
}

func BenchmarkMatcher_ChordIdle(b *testing.B) {
	ts := idleTrieSet(b)
	m, scope := benchMatcherNormal(b, ts)
	const n = 1000
	keys := make([]Key, n)
	for i := range n {
		// runes 'a'..'z' — disjoint from the trie alphabet ('A'..'Z').
		keys[i] = Key{Code: 'a' + rune(i%26)}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := range n {
			res, err := m.Dispatch(scope, keys[j])
			if err != nil {
				b.Fatalf("Dispatch err: %v", err)
			}
			if res != FellThrough {
				b.Fatalf("res=%v want FellThrough at j=%d", res, j)
			}
		}
	}
}

func BenchmarkMatcher_ChordPartial(b *testing.B) {
	ts := partialTrieSet(b)
	m, scope := benchMatcherNormal(b, ts)
	const n = 1000
	// Pattern: every 5th key is 'g' then 'g' (chord), interleaved with
	// fall-through keys. We feed the chord pair adjacent so the second
	// 'g' resolves the leaf.
	keys := make([]Key, n)
	for i := range n {
		switch i % 5 {
		case 0, 1:
			keys[i] = Key{Code: 'g'}
		default:
			keys[i] = Key{Code: 'x'}
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := range n {
			if _, err := m.Dispatch(scope, keys[j]); err != nil {
				b.Fatalf("Dispatch err at j=%d: %v", j, err)
			}
		}
		m.Cancel()
	}
}

// TestMatcher_PasteLatencyUnder20ms guards against keystroke-rate
// regressions: 4000 keys through Dispatch in ModeInsert must complete
// under 20ms (40ms with PGSAVVY_BENCH_RELAX=1). Race builds should pass
// -short to skip — the 100x race overhead trips the threshold by design.
// Threshold detects O(n) per-key regressions; relaxed via
// PGSAVVY_BENCH_RELAX=1; skip via -short.
func TestMatcher_PasteLatencyUnder20ms(t *testing.T) {
	if testing.Short() {
		t.Skip("paste latency test skipped under -short (and -race)")
	}
	defer goleak.VerifyNone(t,
		// dbus client goroutine started by 99designs/keyring (indirect
		// dependency via pkg/session) during package init — pre-existing,
		// unrelated to Matcher state.
		goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
		goleak.IgnoreTopFunction("github.com/godbus/dbus.(*Conn).inWorker"),
		goleak.IgnoreTopFunction("github.com/godbus/dbus.(*Conn).outWorker"),
	)

	threshold := 20 * time.Millisecond
	if os.Getenv("PGSAVVY_BENCH_RELAX") == "1" {
		threshold = 40 * time.Millisecond
	}

	ts := buildTrieSetTB(t, nil)
	m, scope := benchMatcherInsert(t, ts)

	const n = 4000
	keys := make([]Key, n)
	for i := range n {
		keys[i] = Key{Code: 'a' + rune(i%26)}
	}

	start := time.Now()
	for i := range n {
		r, err := m.Dispatch(scope, keys[i])
		if err != nil {
			t.Fatalf("Dispatch err at i=%d: %v", i, err)
		}
		if r != Passthrough && r != FellThrough {
			t.Fatalf("unexpected dispatch result at i=%d: %v", i, r)
		}
	}
	elapsed := time.Since(start)
	if elapsed > threshold {
		t.Fatalf("paste latency: %s > %s (threshold)", elapsed, threshold)
	}
}
