package ui_test

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// updateRecordingDriver captures driver.Update closures and lets the
// test invoke them synchronously so we can observe the auto-clear path
// without flakily depending on the real gocui event loop.
type updateRecordingDriver struct {
	updates atomic.Int64
	mu      sync.Mutex
	fns     []func() error
}

func (d *updateRecordingDriver) Write(_ string, _ []byte) (int, error) { panic("not used") }
func (d *updateRecordingDriver) SetContent(_ string, _ string) error   { panic("not used") }
func (d *updateRecordingDriver) GetViewBuffer(_ string) string         { panic("not used") }
func (d *updateRecordingDriver) SetView(_ string, _, _, _, _ int, _ byte) (types.View, error) {
	panic("not used")
}

func (d *updateRecordingDriver) SetKeybinding(_ string, _ types.Key, _ types.Modifier, _ func() error) error {
	panic("not used")
}

func (d *updateRecordingDriver) SetMasterEditor(_ string, _ gocui.Editor) error {
	panic("not used")
}

func (d *updateRecordingDriver) SetViewClickBinding(_ *types.ViewMouseBinding) error {
	panic("not used")
}

func (d *updateRecordingDriver) Update(fn func() error) {
	d.updates.Add(1)
	d.mu.Lock()
	d.fns = append(d.fns, fn)
	d.mu.Unlock()
}
func (d *updateRecordingDriver) UpdateContentOnly(_ func() error) {}
func (d *updateRecordingDriver) SetCurrentView(_ string) (types.View, error) {
	panic("not used")
}

func (d *updateRecordingDriver) SetViewOnTop(_ string) (types.View, error) {
	panic("not used")
}
func (d *updateRecordingDriver) ViewByName(_ string) (types.View, error) { panic("not used") }
func (d *updateRecordingDriver) DeleteView(_ string) error               { return nil }
func (d *updateRecordingDriver) SetManager(_ ...types.Manager)           {}
func (d *updateRecordingDriver) SetCaretEnabled(_ bool)                  {}
func (d *updateRecordingDriver) SetViewCursor(_ string, _, _ int) error  { return nil }
func (d *updateRecordingDriver) SetViewTabs(_ string, _ []string, _ int) error {
	return nil
}

func (d *updateRecordingDriver) SetTabClickBinding(_ string, _ func(int) error) error {
	return nil
}

func (d *updateRecordingDriver) SetViewTabColors(_ string, _, _ gocui.Attribute) error {
	return nil
}
func (d *updateRecordingDriver) MainLoop() error { return nil }
func (d *updateRecordingDriver) Close() error    { return nil }

// runUpdates invokes every queued closure (FIFO) and clears the buffer.
func (d *updateRecordingDriver) runUpdates(t *testing.T) {
	t.Helper()
	d.mu.Lock()
	fns := d.fns
	d.fns = nil
	d.mu.Unlock()
	for _, fn := range fns {
		if err := fn(); err != nil {
			t.Fatalf("update closure: %v", err)
		}
	}
}

func TestToastShowStoresMessage(t *testing.T) {
	h := ui.NewToastHelper(nil)
	h.Show("hello", 0)
	if got := h.Current(); got != "hello" {
		t.Fatalf("Current = %q; want hello", got)
	}
}

func TestToastShowRedactsDSN(t *testing.T) {
	h := ui.NewToastHelper(nil)
	const dsn = "postgres://alice:hunter2@db.example.com:5432/app"
	h.Show("connect failed: "+dsn, 0)
	got := h.Current()
	if strings.Contains(got, "hunter2") {
		t.Fatalf("toast leaked password: %q", got)
	}
	if !strings.Contains(got, "alice:***@") {
		t.Fatalf("toast not redacted (no alice:***@): %q", got)
	}
}

func TestToastAutoClearViaDriverUpdate(t *testing.T) {
	d := &updateRecordingDriver{}
	h := ui.NewToastHelper(d)

	h.Show("transient", 20*time.Millisecond)
	if got := h.Current(); got != "transient" {
		t.Fatalf("Current right after Show = %q; want transient", got)
	}
	// Wait for the AfterFunc to enqueue the clear via driver.Update.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) && d.updates.Load() == 0 {
		time.Sleep(2 * time.Millisecond)
	}
	if d.updates.Load() == 0 {
		t.Fatalf("driver.Update not invoked; auto-clear didn't fire")
	}
	d.runUpdates(t)
	if got := h.Current(); got != "" {
		t.Fatalf("Current after auto-clear = %q; want empty", got)
	}
}

func TestToastReshowReplacesPending(t *testing.T) {
	d := &updateRecordingDriver{}
	h := ui.NewToastHelper(d)
	h.Show("first", 10*time.Millisecond)
	// Bump the gen with an immediate re-Show; the first timer should
	// become a no-op when its closure finally fires.
	h.Show("second", 200*time.Millisecond)

	// Wait for the first timer to fire & enqueue.
	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) && d.updates.Load() == 0 {
		time.Sleep(2 * time.Millisecond)
	}
	d.runUpdates(t)
	if got := h.Current(); got != "second" {
		t.Fatalf("Current after stale clear = %q; want second", got)
	}
}

func TestToastClearImmediate(t *testing.T) {
	h := ui.NewToastHelper(nil)
	h.Show("x", 0)
	h.Clear()
	if got := h.Current(); got != "" {
		t.Fatalf("Current after Clear = %q; want empty", got)
	}
}

func TestToastConcurrentShowRace(t *testing.T) {
	// 100 concurrent Show() calls — no panic, no data race (run with -race).
	h := ui.NewToastHelper(nil)
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			h.Show("msg", 0)
			_ = h.Current()
		})
	}
	wg.Wait()
	if got := h.Current(); got == "" {
		t.Fatalf("after 100 concurrent Show, Current should still be set; got empty")
	}
}
