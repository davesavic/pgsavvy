package grid

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/davesavic/dbsavvy/pkg/models"
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

// TestYankCell_FocusedCell: `y` yanks only the cell under the cursor and
// pushes it to the clipboard writer.
func TestYankCell_FocusedCell(t *testing.T) {
	v := makeCanonical3x3(t)
	rc := &recordingClipboard{}
	v.SetClipboard(rc)
	v.MoveCursorDown()  // row 1
	v.MoveCursorRight() // col 1 → (1,1) → "e"

	val, ok, err := v.YankCell()
	require.True(t, ok)
	require.NoError(t, err)
	require.Equal(t, "e", val)
	require.Equal(t, "e", rc.lastWrite())
}

// TestYankRow_FocusedRowTSV: `yy` yanks the focused row as TSV across all
// columns regardless of selection state.
func TestYankRow_FocusedRowTSV(t *testing.T) {
	v := makeCanonical3x3(t)
	rc := &recordingClipboard{}
	v.SetClipboard(rc)
	v.MoveCursorDown() // row 1

	val, ok, err := v.YankRow()
	require.True(t, ok)
	require.NoError(t, err)
	require.Equal(t, "d\te\tf", val)
	require.Equal(t, "d\te\tf", rc.lastWrite())
}

// TestYankCell_SanitizesControlBytes: a cell carrying ANSI/control bytes is
// yanked as its sanitized display value (no raw escapes reach the clipboard).
func TestYankCell_SanitizesControlBytes(t *testing.T) {
	v := NewView()
	v.SetColumns([]models.ColumnMeta{{Name: "c0", TypeName: "text"}})
	v.AppendRows([]models.Row{{Values: []any{"x\x1b[31mRED\x07y"}}})
	rc := &recordingClipboard{}
	v.SetClipboard(rc)

	val, ok, _ := v.YankCell()
	require.True(t, ok)
	require.Equal(t, "xREDy", val, "ANSI CSI + BEL must be stripped")
	require.Equal(t, "xREDy", rc.lastWrite())
}

// TestYankCell_EmptyGridNoOp: `y` on an empty grid is a no-op (ok=false, no
// clipboard write, no panic).
func TestYankCell_EmptyGridNoOp(t *testing.T) {
	v := NewView()
	rc := &recordingClipboard{}
	v.SetClipboard(rc)

	val, ok, err := v.YankCell()
	require.False(t, ok)
	require.NoError(t, err)
	require.Empty(t, val)
	require.Empty(t, rc.lastWrite(), "no clipboard write on empty grid")

	rowVal, rowOK, rowErr := v.YankRow()
	require.False(t, rowOK)
	require.NoError(t, rowErr)
	require.Empty(t, rowVal)
}

// TestYankCell_PropagatesClipboardError: a clipboard write error surfaces
// from YankCell so the controller can toast.
func TestYankCell_PropagatesClipboardError(t *testing.T) {
	v := makeCanonical3x3(t)
	rc := &recordingClipboard{err: ErrClipboardTooLarge}
	v.SetClipboard(rc)

	_, ok, err := v.YankCell()
	require.True(t, ok)
	require.ErrorIs(t, err, ErrClipboardTooLarge)
}
