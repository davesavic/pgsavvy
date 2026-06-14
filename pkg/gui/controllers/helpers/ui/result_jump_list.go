package ui

import (
	"sync"
	"time"
)

// DefaultJumpListCapacity is the shipped capacity cap for ResultJumpList.
// Push beyond this evicts the oldest entry (FIFO).
const DefaultJumpListCapacity = 100

// JumpEntry is one position record in the result-tabs jump list. TabSlot
// is the 0-based slot index, TabID the tab's stable monotonic ID (string
// form per the parent design), Row/Col the grid cursor position at push
// time, At the wall-clock stamp, and Tombstone reserved for future
// soft-eviction semantics (currently PruneByTab physically removes).
type JumpEntry struct {
	TabSlot   int
	TabID     string
	Row       int
	Col       int
	At        time.Time
	Tombstone bool
}

// ResultJumpList is a bounded ring/FIFO jump list with vim-like
// Back/Forward semantics:
//
//   - Push appends an entry and truncates any forward history (a new
//     push after Back invalidates the redo stack).
//   - Back walks toward older entries; Forward walks back toward the
//     most recent.
//   - PruneByTab physically removes entries belonging to a closed/evicted
//     tab and clamps the cursor so subsequent Back/Forward stay valid.
//
// All methods are safe for concurrent use; the zero value is NOT usable —
// construct via NewResultJumpList / NewResultJumpListWithCapacity.
type ResultJumpList struct {
	mu       sync.Mutex
	entries  []JumpEntry
	cursor   int // index into entries pointing at current position; -1 = at most-recent
	capacity int
}

// NewResultJumpList returns a list with DefaultJumpListCapacity capacity.
func NewResultJumpList() *ResultJumpList {
	return NewResultJumpListWithCapacity(DefaultJumpListCapacity)
}

// NewResultJumpListWithCapacity returns a list bounded by n. Non-positive
// n falls back to DefaultJumpListCapacity.
func NewResultJumpListWithCapacity(n int) *ResultJumpList {
	if n <= 0 {
		n = DefaultJumpListCapacity
	}
	return &ResultJumpList{
		entries:  make([]JumpEntry, 0, n),
		cursor:   -1,
		capacity: n,
	}
}

// Push appends e. If the list is at capacity the oldest entry is
// evicted FIFO. Any "forward history" (entries past the current cursor,
// produced by an earlier Back) is truncated — standard jumplist
// semantics: a new push after a Back invalidates the redo stack. After
// Push the cursor resets to -1 (at most-recent).
func (l *ResultJumpList) Push(e JumpEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Truncate forward history: if cursor points into the middle of the
	// list, drop everything strictly newer than the cursor so the next
	// Forward returns nothing.
	if l.cursor >= 0 && l.cursor < len(l.entries) {
		l.entries = l.entries[:l.cursor+1]
	}

	// Evict oldest when at capacity.
	if len(l.entries) >= l.capacity {
		// Drop the oldest. Use copy to keep the underlying array bounded.
		copy(l.entries, l.entries[1:])
		l.entries = l.entries[:len(l.entries)-1]
	}

	l.entries = append(l.entries, e)
	l.cursor = -1
}

// Back returns the most-recent non-tombstone entry and advances the
// cursor toward older entries. When the list is empty or every remaining
// entry is tombstoned, returns (zero, false). The caller decides whether
// to toast (e.g. "originating tab evicted") — Back just signals.
func (l *ResultJumpList) Back() (JumpEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.entries) == 0 {
		return JumpEntry{}, false
	}

	// Determine starting search index: if cursor is -1 (at most-recent),
	// start at the tail; otherwise step one older from the current cursor.
	start := len(l.entries) - 1
	if l.cursor >= 0 {
		start = l.cursor - 1
	}

	for i := start; i >= 0; i-- {
		if l.entries[i].Tombstone {
			continue
		}
		l.cursor = i
		return l.entries[i], true
	}
	return JumpEntry{}, false
}

// Forward re-traverses toward the most-recent entry after a Back. Returns
// (zero, false) when at the most-recent position, when the list is
// empty, or when every entry newer than the cursor is tombstoned.
func (l *ResultJumpList) Forward() (JumpEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.entries) == 0 || l.cursor < 0 {
		return JumpEntry{}, false
	}

	for i := l.cursor + 1; i < len(l.entries); i++ {
		if l.entries[i].Tombstone {
			continue
		}
		// Reaching the tail puts us back at "most-recent" (-1) so a
		// subsequent Back walks from the tail again.
		if i == len(l.entries)-1 {
			l.cursor = -1
		} else {
			l.cursor = i
		}
		return l.entries[i], true
	}
	// No non-tombstone entry newer than cursor: nothing to advance to.
	return JumpEntry{}, false
}

// PruneByTab physically removes every entry whose TabID == tabID and
// clamps the cursor to remain valid (clamped to len(entries); -1 when
// all entries removed). We compact rather
// than soft-tombstone — the Tombstone field is retained on JumpEntry as
// documented future use.
func (l *ResultJumpList) PruneByTab(tabID string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.entries) == 0 {
		return
	}

	// Track the absolute index the cursor used to point at so we can
	// re-resolve it against the compacted slice.
	cursorAbs := l.cursor
	out := l.entries[:0]
	newCursor := -1
	for i, e := range l.entries {
		if e.TabID == tabID {
			continue
		}
		// If the original cursor pointed at this surviving entry, that's
		// the new cursor index in the compacted slice.
		if cursorAbs == i {
			newCursor = len(out)
		}
		out = append(out, e)
	}
	l.entries = out

	// If the cursor pointed at a removed entry (or list now empty),
	// reset to -1 (most-recent). Otherwise clamp to the new bounds.
	if newCursor >= len(l.entries) {
		newCursor = -1
	}
	l.cursor = newCursor
}

// Clear empties the list and resets the cursor to -1 (most-recent).
func (l *ResultJumpList) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = l.entries[:0]
	l.cursor = -1
}

// Len returns the current entry count. Test/diagnostic accessor.
func (l *ResultJumpList) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// Capacity returns the configured upper bound. Test accessor.
func (l *ResultJumpList) Capacity() int { return l.capacity }
