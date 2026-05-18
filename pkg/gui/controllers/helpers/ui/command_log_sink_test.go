package ui_test

import (
	"sync"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// writeRecordingDriver captures the writes the sink routes through it.
type writeRecordingDriver struct {
	mu     sync.Mutex
	Writes []writeRec
}

type writeRec struct {
	View string
	Data []byte
}

func (d *writeRecordingDriver) Write(view string, b []byte) (int, error) {
	d.mu.Lock()
	clone := make([]byte, len(b))
	copy(clone, b)
	d.Writes = append(d.Writes, writeRec{View: view, Data: clone})
	d.mu.Unlock()
	return len(b), nil
}

// Remaining types.GuiDriver methods are unused by the sink; panic if
// reached so an accidental call is loud.
func (d *writeRecordingDriver) SetContent(_ string, _ string) error { panic("not used") }
func (d *writeRecordingDriver) GetViewBuffer(_ string) string       { panic("not used") }
func (d *writeRecordingDriver) SetView(_ string, _, _, _, _ int, _ byte) (types.View, error) {
	panic("not used")
}
func (d *writeRecordingDriver) SetKeybinding(_ string, _ types.Key, _ types.Modifier, _ func() error) error {
	panic("not used")
}
func (d *writeRecordingDriver) SetMasterEditor(_ string, _ gocui.Editor) error { panic("not used") }
func (d *writeRecordingDriver) SetViewClickBinding(_ *types.ViewMouseBinding) error {
	panic("not used")
}
func (d *writeRecordingDriver) Update(_ func() error)            { panic("not used") }
func (d *writeRecordingDriver) UpdateContentOnly(_ func() error) { panic("not used") }
func (d *writeRecordingDriver) SetCurrentView(_ string) (types.View, error) {
	panic("not used")
}
func (d *writeRecordingDriver) SetViewOnTop(_ string) (types.View, error) { panic("not used") }
func (d *writeRecordingDriver) ViewByName(_ string) (types.View, error)   { panic("not used") }
func (d *writeRecordingDriver) DeleteView(_ string) error                 { return nil }
func (d *writeRecordingDriver) SetManager(_ ...types.Manager)             {}
func (d *writeRecordingDriver) SetCaretEnabled(_ bool)                    {}
func (d *writeRecordingDriver) SetViewCursor(_ string, _, _ int) error    { return nil }
func (d *writeRecordingDriver) MainLoop() error                           { return nil }
func (d *writeRecordingDriver) Close() error                              { return nil }

func TestDefaultCommandLogSink_AppendRoutesThroughUIThread(t *testing.T) {
	d := &writeRecordingDriver{}
	// Capture scheduling: the sink hands a closure to onUIThreadContentOnly;
	// the test invokes it synchronously to assert the eventual driver.Write.
	var scheduled []func() error
	onUIThreadContentOnly := func(fn func() error) {
		scheduled = append(scheduled, fn)
	}
	sink := ui.NewDefaultCommandLogSink(d, onUIThreadContentOnly)

	sink.Append("line one")

	if len(scheduled) != 1 {
		t.Fatalf("scheduled count = %d; want 1", len(scheduled))
	}
	// Driver hasn't been touched yet — the sink scheduled the write
	// rather than firing it inline.
	if got := len(d.Writes); got != 0 {
		t.Fatalf("writes before flush = %d; want 0", got)
	}
	if err := scheduled[0](); err != nil {
		t.Fatalf("scheduled fn: %v", err)
	}
	if got := len(d.Writes); got != 1 {
		t.Fatalf("writes after flush = %d; want 1", got)
	}
	w := d.Writes[0]
	if w.View != string(types.LOG) {
		t.Errorf("view = %q; want LOG (%q)", w.View, string(types.LOG))
	}
	if string(w.Data) != "line one\n" {
		t.Errorf("payload = %q; want %q", string(w.Data), "line one\n")
	}
}

func TestDefaultCommandLogSink_NilDriverNoOp(t *testing.T) {
	sink := ui.NewDefaultCommandLogSink(nil, nil)
	// Must not panic.
	sink.Append("dropped")
}

func TestDefaultCommandLogSink_NilSchedulerWritesSynchronously(t *testing.T) {
	d := &writeRecordingDriver{}
	sink := ui.NewDefaultCommandLogSink(d, nil)
	sink.Append("sync")
	if got := len(d.Writes); got != 1 {
		t.Fatalf("writes = %d; want 1 (synchronous fallback)", got)
	}
	if string(d.Writes[0].Data) != "sync\n" {
		t.Errorf("payload = %q; want %q", string(d.Writes[0].Data), "sync\n")
	}
}
