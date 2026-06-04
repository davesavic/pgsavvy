package ui

import (
	"time"

	"github.com/davesavic/dbsavvy/pkg/gui/editor"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// YankFlashHelper arms the transient post-yank highlight (Neovim on_yank
// parity) and schedules its auto-clear. It holds no generation counter of
// its own: the stale-timer guard lives on the Buffer via the epoch returned
// from SetYankFlash, so a later yank that re-arms the flash makes any
// in-flight clear from an earlier flash a no-op.
type YankFlashHelper struct {
	driver types.GuiDriver
}

// NewYankFlashHelper constructs the helper around the runtime driver. A nil
// driver leaves the helper functional for SetYankFlash but skips the
// auto-clear scheduling (the flash then persists until the next render
// clears it).
func NewYankFlashHelper(driver types.GuiDriver) *YankFlashHelper {
	return &YankFlashHelper{driver: driver}
}

// Flash sets the post-yank highlight range on buf and arms a delayed clear
// after ttl. The SetYankFlash call always runs (so the highlight shows even
// without a wired driver); only the AfterFunc scheduling is guarded by a nil
// driver / non-positive ttl. The Buffer's epoch makes a stale timer-fire a
// no-op.
func (h *YankFlashHelper) Flash(buf *editor.Buffer, r editor.Range, ttl time.Duration) {
	if buf == nil {
		return
	}
	epoch := buf.SetYankFlash(r)
	if ttl <= 0 || h.driver == nil {
		return
	}
	time.AfterFunc(ttl, func() {
		h.driver.Update(func() error {
			buf.ClearYankFlash(epoch)
			return nil
		})
	})
}
