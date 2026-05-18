package grid

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// recordingClipboard captures the most recent Write payload for assertion.
// Safe for concurrent Write — tests only inspect after the goroutine they
// spawned has returned, but we lock anyway so the -race build stays clean.
type recordingClipboard struct {
	mu  sync.Mutex
	got string
	err error
}

func (rc *recordingClipboard) Write(text string) error {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.got = text
	return rc.err
}

func (rc *recordingClipboard) lastWrite() string {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.got
}

// TestYank_RowSelectionTSV: row-mode yank on row 0 returns the entire
// first row joined with tabs.
func TestYank_RowSelectionTSV(t *testing.T) {
	v := makeCanonical3x3(t)
	v.EnterRowMode()
	require.Equal(t, "a\tb\tc", v.Yank())
}

// TestYank_BlockSelectionTSV: 2x2 block selection at the top-left
// produces a tab-separated, newline-joined rectangle.
func TestYank_BlockSelectionTSV(t *testing.T) {
	v := makeCanonical3x3(t)
	v.EnterBlockMode()
	v.MoveCursorDown()
	v.MoveCursorRight()
	require.Equal(t, "a\tb\nd\te", v.Yank())
}

// TestYank_NoSelectionFallsBackToCursorCell: without any selection,
// Yank returns the cell under the cursor — (1,2) → "f".
func TestYank_NoSelectionFallsBackToCursorCell(t *testing.T) {
	v := makeCanonical3x3(t)
	v.MoveCursorDown()  // row 1
	v.MoveCursorRight() // col 1
	v.MoveCursorRight() // col 2 → cursor at (1,2) → "f"
	require.Equal(t, SelectionNone, v.SelectionMode())
	require.Equal(t, "f", v.Yank())
}

// TestYank_InvokesClipboardWriter: SetClipboard installs a recording
// writer; the next Yank must hand the same string to the writer as it
// returns to the caller.
func TestYank_InvokesClipboardWriter(t *testing.T) {
	v := makeCanonical3x3(t)
	rc := &recordingClipboard{}
	v.SetClipboard(rc)

	v.EnterRowMode()
	got := v.Yank()
	require.Equal(t, "a\tb\tc", got)
	require.Equal(t, got, rc.lastWrite(),
		"clipboard writer should receive the same string Yank returns")
}
