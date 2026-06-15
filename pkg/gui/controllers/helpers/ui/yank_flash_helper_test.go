package ui_test

import (
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/gui/editor"
)

// waitForUpdates blocks until the recording driver has captured at least n
// Update closures (the AfterFunc-scheduled clears) or the deadline elapses.
// Keeps the stale-epoch assertion deterministic without asserting wall-clock
// timing of the flash itself.
func waitForUpdates(t *testing.T, d *updateRecordingDriver, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if int(d.updates.Load()) >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d Update closures (got %d)", n, d.updates.Load())
}

// runNextUpdate invokes the single oldest queued closure (FIFO) and removes
// it, leaving any remaining closures for a later call. Lets the stale-epoch
// test run the two scheduled clears one at a time.
func (d *updateRecordingDriver) runNextUpdate(t *testing.T) {
	t.Helper()
	d.mu.Lock()
	if len(d.fns) == 0 {
		d.mu.Unlock()
		t.Fatalf("no queued Update closure to run")
	}
	fn := d.fns[0]
	d.fns = d.fns[1:]
	d.mu.Unlock()
	if err := fn(); err != nil {
		t.Fatalf("update closure: %v", err)
	}
}

func yankFlashBuf() *editor.Buffer {
	b := editor.NewBuffer()
	b.Lines = []editor.Line{{Runes: []rune("hello world")}}
	return b
}

// Flash sets the buffer flash range, observable via YankFlashSnapshot.
func TestYankFlashHelper_FlashSetsRange(t *testing.T) {
	d := &updateRecordingDriver{}
	h := ui.NewYankFlashHelper(d)
	buf := yankFlashBuf()
	r := editor.Range{Start: editor.Position{Line: 0, Col: 0}, End: editor.Position{Line: 0, Col: 5}}

	h.Flash(buf, r, time.Hour) // long ttl: clear won't fire during the test

	snap := buf.YankFlashSnapshot()
	if snap == nil {
		t.Fatalf("expected flash range set, got nil")
	}
	if snap.End.Col != 5 {
		t.Fatalf("flash End.Col = %d, want 5", snap.End.Col)
	}
}

// A nil driver still applies the flash (so the highlight shows until the next
// render); only the AfterFunc scheduling is skipped. Must not panic.
func TestYankFlashHelper_NilDriverStillFlashes(t *testing.T) {
	h := ui.NewYankFlashHelper(nil)
	buf := yankFlashBuf()
	r := editor.Range{Start: editor.Position{Line: 0, Col: 0}, End: editor.Position{Line: 0, Col: 3}}

	h.Flash(buf, r, 10*time.Millisecond)

	if snap := buf.YankFlashSnapshot(); snap == nil {
		t.Fatalf("nil-driver Flash should still SetYankFlash, got nil snapshot")
	}
}

// Stale-epoch guard: a second Flash within the TTL re-arms the flash with a
// fresh epoch, so the FIRST scheduled clear is a no-op while the SECOND
// clears. Asserted via YankFlashSnapshot, not internal state. The two clears
// are driven through the recording driver and invoked in FIFO order.
func TestYankFlashHelper_StaleEpochFirstClearNoOps(t *testing.T) {
	d := &updateRecordingDriver{}
	h := ui.NewYankFlashHelper(d)
	buf := yankFlashBuf()
	r1 := editor.Range{Start: editor.Position{Line: 0, Col: 0}, End: editor.Position{Line: 0, Col: 3}}
	r2 := editor.Range{Start: editor.Position{Line: 0, Col: 0}, End: editor.Position{Line: 0, Col: 5}}

	// Tiny ttl so both AfterFuncs queue their clears into the recorder
	// quickly; the recorder defers execution so order is preserved.
	h.Flash(buf, r1, time.Millisecond)
	h.Flash(buf, r2, time.Millisecond)
	waitForUpdates(t, d, 2)

	// Snapshot before running any clear: the second Flash won (epoch 2),
	// range covers cols 0..5.
	snap := buf.YankFlashSnapshot()
	if snap == nil || snap.End.Col != 5 {
		t.Fatalf("active flash before clears = %+v, want End.Col 5", snap)
	}

	// Run the FIRST queued clear in isolation (epoch 1, stale). It must NOT
	// clear the live epoch-2 flash — this is the stale-timer guard.
	d.runNextUpdate(t)
	if snap := buf.YankFlashSnapshot(); snap == nil {
		t.Fatalf("stale first clear wrongly cleared the epoch-2 flash")
	}

	// Run the SECOND clear (epoch 2, current). It clears.
	d.runNextUpdate(t)
	if snap := buf.YankFlashSnapshot(); snap != nil {
		t.Fatalf("current-epoch clear left flash set: %+v", snap)
	}
}
