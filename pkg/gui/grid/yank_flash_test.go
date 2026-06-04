package grid

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/davesavic/dbsavvy/pkg/models"
)

func yankFlashTestView() *View {
	v := NewView()
	v.SetColumns([]models.ColumnMeta{
		{Name: "a", TypeName: "text"},
		{Name: "b", TypeName: "text"},
		{Name: "c", TypeName: "text"},
	})
	v.AppendRows([]models.Row{
		{Values: []any{"a0", "b0", "c0"}},
		{Values: []any{"a1", "b1", "c1"}},
	})
	v.widths = []int{6, 6, 6}
	return v
}

// TestFlashYankCell_ArmsSingleCell proves a cell yank flash covers only the
// focused cell and is visible in the snapshot the renderer reads.
func TestFlashYankCell_ArmsSingleCell(t *testing.T) {
	v := yankFlashTestView()
	v.SetCursor(1, 1)

	epoch := v.FlashYankCell()
	require.NotZero(t, epoch, "arming a flash on a non-empty grid returns a live epoch")

	snap := v.snapshot()
	require.True(t, inYankFlash(snap, 1, 1), "focused cell is flashed")
	require.False(t, inYankFlash(snap, 1, 0), "neighbouring cell in the same row is not")
	require.False(t, inYankFlash(snap, 0, 1), "neighbouring cell in the same column is not")
}

// TestFlashYankRow_ArmsWholeRow proves a row yank flash covers every column
// of the focused row and no other row.
func TestFlashYankRow_ArmsWholeRow(t *testing.T) {
	v := yankFlashTestView()
	v.SetCursor(0, 0)

	epoch := v.FlashYankRow()
	require.NotZero(t, epoch)

	snap := v.snapshot()
	for c := 0; c < 3; c++ {
		require.True(t, inYankFlash(snap, 0, c), "every column of the focused row is flashed (col %d)", c)
	}
	require.False(t, inYankFlash(snap, 1, 0), "the other row is not flashed")
}

// TestFlashYank_EmptyGridNoop proves arming on an empty grid is a no-op
// (epoch 0) so the caller skips scheduling a clear.
func TestFlashYank_EmptyGridNoop(t *testing.T) {
	v := NewView()
	require.Zero(t, v.FlashYankCell())
	require.Zero(t, v.FlashYankRow())
	require.Nil(t, v.snapshot().yankFlash)
}

// TestClearYankFlash_EpochGuard proves a stale clear (from an earlier flash)
// is a no-op once a newer yank has re-armed the highlight, while the matching
// clear drops it.
func TestClearYankFlash_EpochGuard(t *testing.T) {
	v := yankFlashTestView()
	v.SetCursor(0, 0)

	stale := v.FlashYankCell()
	v.SetCursor(1, 2)
	fresh := v.FlashYankCell()
	require.NotEqual(t, stale, fresh)

	v.ClearYankFlash(stale) // stale timer fires after the re-arm
	require.NotNil(t, v.snapshot().yankFlash, "stale clear must not drop the newer flash")

	v.ClearYankFlash(fresh)
	require.Nil(t, v.snapshot().yankFlash, "matching clear drops the flash")
}

// TestRenderDataLine_YankFlashTint proves the flashed cell renders with the
// yellow yank-flash background and an unflashed cell does not.
func TestRenderDataLine_YankFlashTint(t *testing.T) {
	v := yankFlashTestView()
	v.SetCursor(0, 1)
	v.FlashYankCell()

	const innerW = 60
	snap := v.snapshot()
	line := renderDataLine(snap, 0, innerW)
	require.Contains(t, line, ansiYankFlashBg, "the flashed row renders the yank-flash background")

	// Row 1 carries no flash → no yank-flash escape.
	other := renderDataLine(snap, 1, innerW)
	require.False(t, strings.Contains(other, ansiYankFlashBg), "an unflashed row carries no yank-flash background")
}
