package keys_test

import (
	"errors"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// fakeDriver is a tiny GuiDriver fake that records SetKeybinding calls.
// Other GuiDriver methods panic so a test inadvertently exercising them
// fails loudly.
type fakeDriver struct {
	calls  []bindingRecord
	setErr error
}

type bindingRecord struct {
	View string
	Key  types.Key
	Mod  types.Modifier
}

func (f *fakeDriver) SetKeybinding(view string, key types.Key, mod types.Modifier, _ func() error) error {
	f.calls = append(f.calls, bindingRecord{View: view, Key: key, Mod: mod})
	return f.setErr
}

func (f *fakeDriver) Write(_ string, _ []byte) (int, error) { panic("not used") }
func (f *fakeDriver) SetContent(_ string, _ string) error   { panic("not used") }
func (f *fakeDriver) GetViewBuffer(_ string) string         { panic("not used") }
func (f *fakeDriver) SetView(_ string, _, _, _, _ int, _ byte) (types.View, error) {
	panic("not used")
}
func (f *fakeDriver) SetMasterEditor(_ string, _ gocui.Editor) error      { panic("not used") }
func (f *fakeDriver) SetViewClickBinding(_ *types.ViewMouseBinding) error { panic("not used") }
func (f *fakeDriver) Update(_ func() error)                               {}
func (f *fakeDriver) UpdateContentOnly(_ func() error)                    {}
func (f *fakeDriver) SetCurrentView(_ string) (types.View, error)         { panic("not used") }
func (f *fakeDriver) SetViewOnTop(_ string) (types.View, error)           { panic("not used") }
func (f *fakeDriver) ViewByName(_ string) (types.View, error)             { panic("not used") }
func (f *fakeDriver) DeleteView(_ string) error                           { return nil }
func (f *fakeDriver) SetManager(_ ...types.Manager)                       {}
func (f *fakeDriver) MainLoop() error                                     { return nil }
func (f *fakeDriver) Close() error                                        { return nil }

type recordingLogger struct{ msgs []string }

func (r *recordingLogger) Debugf(format string, args ...any) {
	r.msgs = append(r.msgs, format)
	_ = args
}

func TestRegisterCallsSetKeybindingOnDriver(t *testing.T) {
	d := &fakeDriver{}
	called := false
	handler := func() error { called = true; return nil }

	if err := keys.Register(d, nil, "connections", gocui.NewKeyRune('j'), gocui.ModNone, handler, "Down"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if len(d.calls) != 1 {
		t.Fatalf("got %d SetKeybinding calls, want 1", len(d.calls))
	}
	if d.calls[0].View != "connections" {
		t.Fatalf("View=%q want connections", d.calls[0].View)
	}
	if !d.calls[0].Key.Equals(gocui.NewKeyRune('j')) {
		t.Fatalf("Key=%v want gocui.NewKeyRune('j')", d.calls[0].Key)
	}
	if d.calls[0].Mod != gocui.ModNone {
		t.Fatalf("Mod=%v want ModNone", d.calls[0].Mod)
	}
	// Handler was not invoked by Register itself.
	if called {
		t.Fatal("handler should not be invoked by Register")
	}
}

func TestRegisterNilDriverIsNoop(t *testing.T) {
	if err := keys.Register(nil, nil, "v", gocui.NewKeyRune('q'), gocui.ModNone, func() error { return nil }, "x"); err != nil {
		t.Fatalf("Register(nil driver): %v", err)
	}
}

func TestRegisterPropagatesDriverError(t *testing.T) {
	want := errors.New("boom")
	d := &fakeDriver{setErr: want}
	err := keys.Register(d, nil, "v", gocui.NewKeyRune('k'), gocui.ModNone, func() error { return nil }, "Up")
	if !errors.Is(err, want) {
		t.Fatalf("Register err = %v, want %v", err, want)
	}
}

func TestRegisterLogsWhenLoggerPresent(t *testing.T) {
	d := &fakeDriver{}
	rl := &recordingLogger{}
	if err := keys.Register(d, rl, "schemas", gocui.NewKeyRune('H'), gocui.ModNone, func() error { return nil }, "Hide Schema"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if len(rl.msgs) == 0 {
		t.Fatal("expected logger.Debugf to be invoked, got 0 calls")
	}
}
