package keys

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// TestMatcher_RaceDispatchCancelSwap stresses the Matcher with
// concurrent Dispatch + Cancel + SwapTrieSet goroutines. Run with
// `go test -race`; this exercises mutex ordering and the AfterFunc
// stale-fire suppression.
func TestMatcher_RaceDispatchCancelSwap(t *testing.T) {
	cmdLeaf := fixtureCmd("test.leaf")
	cmdLong := fixtureCmd("test.long")
	build := func() *TrieSet {
		return buildTrieSet(t, []trieEntry{
			{types.ModeNormal, types.QUERY_EDITOR, []Key{keyOf('z')}, cmdLeaf},
			{types.ModeNormal, types.QUERY_EDITOR, []Key{keyOf('z'), keyOf('z')}, cmdLong},
			{types.ModeNormal, types.GLOBAL, []Key{keyOf('q')}, fixtureCmd("test.global")},
		})
	}
	store := NewModeStore()
	store.Set(types.QUERY_EDITOR, types.ModeNormal)
	m, err := NewMatcher(build(), MatcherConfig{
		Modes:       store,
		TimeoutLen:  2 * time.Millisecond,
		TtimeoutLen: 1 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}

	const goroutines = 50
	const iterations = 100
	var wg sync.WaitGroup
	var stop atomic.Bool

	// Dispatch goroutines.
	for i := range goroutines {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			keys := []Key{keyOf('z'), keyOf('q'), keyOf('x'), keyOf('5'), keyOf('j')}
			for n := 0; n < iterations && !stop.Load(); n++ {
				k := keys[(seed+n)%len(keys)]
				_, _ = m.Dispatch(types.QUERY_EDITOR, k)
			}
		}(i)
	}

	// Cancel goroutine.
	wg.Go(func() {
		for n := 0; n < iterations*5 && !stop.Load(); n++ {
			m.Cancel()
			time.Sleep(50 * time.Microsecond)
		}
	})

	// SwapTrieSet goroutine.
	wg.Go(func() {
		for n := 0; n < 20 && !stop.Load(); n++ {
			m.SwapTrieSet(build())
			time.Sleep(500 * time.Microsecond)
		}
	})

	// Bail-out timer in case some path deadlocks under the race detector.
	go func() {
		time.Sleep(10 * time.Second)
		stop.Store(true)
	}()

	wg.Wait()
}

// TestMatcher_RaceTimerFireVsCancel reproduces the canonical race: a
// pending state whose timer fires concurrently with Cancel. The
// AfterFunc id check must suppress the stale fire.
func TestMatcher_RaceTimerFireVsCancel(t *testing.T) {
	cmdLeaf := fixtureCmd("test.leaf")
	cmdLong := fixtureCmd("test.long")
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeNormal, types.QUERY_EDITOR, []Key{keyOf('z')}, cmdLeaf},
		{types.ModeNormal, types.QUERY_EDITOR, []Key{keyOf('z'), keyOf('z')}, cmdLong},
	})
	store := NewModeStore()
	store.Set(types.QUERY_EDITOR, types.ModeNormal)
	m, err := NewMatcher(ts, MatcherConfig{
		Modes:      store,
		TimeoutLen: 1 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}

	for range 1000 {
		if _, err := m.Dispatch(types.QUERY_EDITOR, keyOf('z')); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		m.Cancel()
	}
}

// TestMatcher_RaceInsertPendingFlushRegister exercises the
// OnInsertPendingFlush registration concurrently with Dispatch in
// ModeInsert.
func TestMatcher_RaceInsertPendingFlushRegister(t *testing.T) {
	jk := fixtureCmd("mode.normal_from_insert")
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeInsert, types.QUERY_EDITOR, []Key{keyOf('j'), keyOf('k')}, jk},
	})
	store := NewModeStore()
	store.Set(types.QUERY_EDITOR, types.ModeInsert)
	m, err := NewMatcher(ts, MatcherConfig{
		Modes:      store,
		TimeoutLen: 1 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		for range 500 {
			m.OnInsertPendingFlush(types.QUERY_EDITOR, func(types.ContextKey, []rune) {})
		}
	}()
	go func() {
		defer wg.Done()
		for range 500 {
			m.OnInsertPendingFlush(types.QUERY_EDITOR, nil)
		}
	}()
	go func() {
		defer wg.Done()
		for range 500 {
			_, _ = m.Dispatch(types.QUERY_EDITOR, keyOf('j'))
			time.Sleep(50 * time.Microsecond)
		}
	}()

	wg.Wait()
}

var _ commands.Handler = func(commands.ExecCtx) error { return nil } // imported-symbol guard
