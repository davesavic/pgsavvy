package ui

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// helper: build an entry with just the bits the list cares about.
func je(tabID string, row int) JumpEntry {
	return JumpEntry{
		TabSlot: 0,
		TabID:   tabID,
		Row:     row,
		Col:     0,
		At:      time.Unix(0, int64(row)),
	}
}

// 1. Push to empty: Back returns the just-pushed entry.
func TestResultJumpList_PushToEmpty(t *testing.T) {
	l := NewResultJumpList()
	l.Push(je("a", 1))
	got, ok := l.Back()
	if !ok {
		t.Fatalf("Back after first Push should return entry, got !ok")
	}
	if got.TabID != "a" || got.Row != 1 {
		t.Fatalf("Back returned %+v, want TabID=a Row=1", got)
	}
}

// 2. Push N, Back returns most recent; Back again returns N-1.
func TestResultJumpList_BackWalksOlder(t *testing.T) {
	l := NewResultJumpList()
	l.Push(je("a", 1))
	l.Push(je("b", 2))
	l.Push(je("c", 3))

	got, ok := l.Back()
	if !ok || got.TabID != "c" {
		t.Fatalf("first Back: got %+v ok=%v, want TabID=c", got, ok)
	}
	got, ok = l.Back()
	if !ok || got.TabID != "b" {
		t.Fatalf("second Back: got %+v ok=%v, want TabID=b", got, ok)
	}
	got, ok = l.Back()
	if !ok || got.TabID != "a" {
		t.Fatalf("third Back: got %+v ok=%v, want TabID=a", got, ok)
	}
	if _, ok := l.Back(); ok {
		t.Fatalf("fourth Back: want !ok past oldest")
	}
}

// 3. Forward after Back returns to top.
func TestResultJumpList_ForwardReturnsToTop(t *testing.T) {
	l := NewResultJumpList()
	l.Push(je("a", 1))
	l.Push(je("b", 2))
	l.Push(je("c", 3))

	// Walk back two steps then forward two steps.
	_, _ = l.Back() // -> c
	_, _ = l.Back() // -> b
	got, ok := l.Forward()
	if !ok || got.TabID != "c" {
		t.Fatalf("Forward after 2x Back: got %+v ok=%v, want c", got, ok)
	}
	// At tail (cursor reset to -1): no further Forward.
	if _, ok := l.Forward(); ok {
		t.Fatalf("Forward at most-recent: want !ok")
	}
}

// 4. Push to full capacity: oldest evicted FIFO.
func TestResultJumpList_CapacityEviction(t *testing.T) {
	l := NewResultJumpListWithCapacity(3)
	l.Push(je("a", 1))
	l.Push(je("b", 2))
	l.Push(je("c", 3))
	l.Push(je("d", 4)) // evicts "a"

	if got := l.Len(); got != 3 {
		t.Fatalf("Len after overflow: got %d want 3", got)
	}
	// Walk all the way back; should see d, c, b (no a).
	want := []string{"d", "c", "b"}
	for i, w := range want {
		got, ok := l.Back()
		if !ok || got.TabID != w {
			t.Fatalf("Back[%d]: got %+v ok=%v, want %s", i, got, ok, w)
		}
	}
	if _, ok := l.Back(); ok {
		t.Fatalf("Back past oldest: want !ok (a was evicted)")
	}
}

// 5. PruneByTab on empty: no-op, no panic.
func TestResultJumpList_PruneEmpty(t *testing.T) {
	l := NewResultJumpList()
	l.PruneByTab("nonexistent")
	if l.Len() != 0 {
		t.Fatalf("Len after prune-empty: got %d want 0", l.Len())
	}
	if _, ok := l.Back(); ok {
		t.Fatalf("Back on empty after prune: want !ok")
	}
}

// 6. PruneByTab removes matching entries; Back returns next non-pruned.
func TestResultJumpList_PruneRemovesAndAdvances(t *testing.T) {
	l := NewResultJumpList()
	l.Push(je("a", 1))
	l.Push(je("b", 2))
	l.Push(je("a", 3))
	l.Push(je("c", 4))

	l.PruneByTab("a")
	if l.Len() != 2 {
		t.Fatalf("Len after pruning a: got %d want 2", l.Len())
	}
	got, ok := l.Back()
	if !ok || got.TabID != "c" {
		t.Fatalf("Back after prune: got %+v ok=%v, want c", got, ok)
	}
	got, ok = l.Back()
	if !ok || got.TabID != "b" {
		t.Fatalf("Back2 after prune: got %+v ok=%v, want b", got, ok)
	}
	if _, ok := l.Back(); ok {
		t.Fatalf("Back past pruned: want !ok")
	}
}

// 7. Back when all entries pruned: returns (_, false).
func TestResultJumpList_BackAfterAllPruned(t *testing.T) {
	l := NewResultJumpList()
	l.Push(je("a", 1))
	l.Push(je("a", 2))
	l.PruneByTab("a")
	if _, ok := l.Back(); ok {
		t.Fatalf("Back after prune-all: want !ok")
	}
}

//  8. Concurrent Push + PruneByTab + Back from 16 goroutines x 1000 ops
//     with -race must be clean. The assertion is "no panic, no race
//     detector hit"; the resulting list state is non-deterministic.
func TestResultJumpList_ConcurrentSafe(t *testing.T) {
	l := NewResultJumpListWithCapacity(50)
	const goroutines = 16
	const ops = 1000

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := range goroutines {
		go func() {
			defer wg.Done()
			for i := range ops {
				switch i % 4 {
				case 0:
					l.Push(je(fmt.Sprintf("tab%d", g%4), i))
				case 1:
					_, _ = l.Back()
				case 2:
					_, _ = l.Forward()
				case 3:
					l.PruneByTab(fmt.Sprintf("tab%d", (g+1)%4))
				}
			}
		}()
	}
	wg.Wait()
}

//  9. Cursor invariant: after Back then Push, Forward returns (_, false)
//     (truncated forward history).
func TestResultJumpList_PushTruncatesForward(t *testing.T) {
	l := NewResultJumpList()
	l.Push(je("a", 1))
	l.Push(je("b", 2))
	l.Push(je("c", 3))

	_, _ = l.Back() // cursor -> c (index 2)
	_, _ = l.Back() // cursor -> b (index 1)
	// Push after Back must truncate c (the "forward history") so Forward
	// finds nothing.
	l.Push(je("d", 4))

	if _, ok := l.Forward(); ok {
		t.Fatalf("Forward after Push-after-Back: want !ok (truncated)")
	}
	// Sanity: Back now sees d, then b, then a (no c).
	want := []string{"d", "b", "a"}
	for i, w := range want {
		got, ok := l.Back()
		if !ok || got.TabID != w {
			t.Fatalf("Back[%d]: got %+v ok=%v, want %s", i, got, ok, w)
		}
	}
}

// 10. Cleared list: Back / Forward both (_, false).
func TestResultJumpList_Clear(t *testing.T) {
	l := NewResultJumpList()
	l.Push(je("a", 1))
	l.Push(je("b", 2))
	l.Clear()
	if l.Len() != 0 {
		t.Fatalf("Len after Clear: got %d want 0", l.Len())
	}
	if _, ok := l.Back(); ok {
		t.Fatalf("Back after Clear: want !ok")
	}
	if _, ok := l.Forward(); ok {
		t.Fatalf("Forward after Clear: want !ok")
	}
}

// Extra: PruneByTab clamps the cursor when the entry it pointed at is
// removed — a subsequent Back should walk from the new tail.
func TestResultJumpList_PruneResetsCursor(t *testing.T) {
	l := NewResultJumpList()
	l.Push(je("a", 1))
	l.Push(je("b", 2))
	l.Push(je("c", 3))

	_, _ = l.Back() // cursor at c
	l.PruneByTab("c")
	// Cursor was at the removed entry; next Back should land on b.
	got, ok := l.Back()
	if !ok || got.TabID != "b" {
		t.Fatalf("Back after pruning cursor entry: got %+v ok=%v, want b", got, ok)
	}
}

// Extra: NewResultJumpList default capacity.
func TestResultJumpList_DefaultCapacity(t *testing.T) {
	l := NewResultJumpList()
	if l.Capacity() != DefaultJumpListCapacity {
		t.Fatalf("default capacity: got %d want %d", l.Capacity(), DefaultJumpListCapacity)
	}
}

// Extra: non-positive capacity falls back to default.
func TestResultJumpList_CapacityFallback(t *testing.T) {
	l := NewResultJumpListWithCapacity(0)
	if l.Capacity() != DefaultJumpListCapacity {
		t.Fatalf("zero capacity should fall back to default, got %d", l.Capacity())
	}
	l = NewResultJumpListWithCapacity(-5)
	if l.Capacity() != DefaultJumpListCapacity {
		t.Fatalf("negative capacity should fall back to default, got %d", l.Capacity())
	}
}
