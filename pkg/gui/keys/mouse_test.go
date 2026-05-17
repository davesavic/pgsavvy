package keys_test

import (
	"errors"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// mouseFakeDriver records SetViewClickBinding calls and returns the
// configured error. Implements only the methods the mouse helper uses.
type mouseFakeDriver struct {
	bindings []*types.ViewMouseBinding
	err      error
}

func (m *mouseFakeDriver) SetViewClickBinding(b *types.ViewMouseBinding) error {
	m.bindings = append(m.bindings, b)
	return m.err
}

// The rest of GuiDriver — all panic so unintended use is loud.
func (m *mouseFakeDriver) Write(_ string, _ []byte) (int, error) { panic("not used") }
func (m *mouseFakeDriver) SetContent(_ string, _ string) error   { panic("not used") }
func (m *mouseFakeDriver) GetViewBuffer(_ string) string         { panic("not used") }
func (m *mouseFakeDriver) SetView(_ string, _, _, _, _ int, _ byte) (types.View, error) {
	panic("not used")
}

func (m *mouseFakeDriver) SetKeybinding(_ string, _ types.Key, _ types.Modifier, _ func() error) error {
	panic("not used")
}
func (m *mouseFakeDriver) SetMasterEditor(_ string, _ gocui.Editor) error { panic("not used") }
func (m *mouseFakeDriver) Update(_ func() error)                          {}
func (m *mouseFakeDriver) UpdateContentOnly(_ func() error)               {}
func (m *mouseFakeDriver) SetCurrentView(_ string) (types.View, error) {
	panic("not used")
}
func (m *mouseFakeDriver) SetViewOnTop(_ string) (types.View, error) { panic("not used") }
func (m *mouseFakeDriver) ViewByName(_ string) (types.View, error)   { panic("not used") }
func (m *mouseFakeDriver) DeleteView(_ string) error                 { return nil }
func (m *mouseFakeDriver) SetManager(_ ...types.Manager)             {}
func (m *mouseFakeDriver) MainLoop() error                           { return nil }
func (m *mouseFakeDriver) Close() error                              { return nil }

type warnRecorder struct{ msgs []string }

func (w *warnRecorder) Warnf(format string, _ ...any) {
	w.msgs = append(w.msgs, format)
}

func TestRegisterMouseBindingHappyPath(t *testing.T) {
	keys.ResetMouseWarnOnceForTest()
	d := &mouseFakeDriver{}
	called := false
	handler := func(types.ViewMouseBindingOpts) error { called = true; return nil }
	if err := keys.RegisterMouseBinding(d, &warnRecorder{}, "schemas", gocui.MouseLeft, gocui.ModNone, handler, "Focus"); err != nil {
		t.Fatalf("RegisterMouseBinding: %v", err)
	}
	if len(d.bindings) != 1 {
		t.Fatalf("bindings = %d; want 1", len(d.bindings))
	}
	if d.bindings[0].ViewName != "schemas" {
		t.Fatalf("ViewName = %q; want schemas", d.bindings[0].ViewName)
	}
	if called {
		t.Fatal("handler should not be invoked by RegisterMouseBinding")
	}
}

func TestRegisterMouseBindingNilDriverNoop(t *testing.T) {
	keys.ResetMouseWarnOnceForTest()
	if err := keys.RegisterMouseBinding(nil, nil, "v", gocui.MouseLeft, gocui.ModNone, nil, "x"); err != nil {
		t.Fatalf("RegisterMouseBinding(nil): %v", err)
	}
}

func TestRegisterMouseBindingSwallowsErrorAndLogsOnce(t *testing.T) {
	keys.ResetMouseWarnOnceForTest()
	d := &mouseFakeDriver{err: errors.New("unsupported mouse mode")}
	w := &warnRecorder{}
	for i := range 3 {
		if err := keys.RegisterMouseBinding(d, w, "v", gocui.MouseLeft, gocui.ModNone, nil, "x"); err != nil {
			t.Fatalf("RegisterMouseBinding[%d] returned err = %v; want swallowed", i, err)
		}
	}
	// All 3 calls reached the driver; only 1 Warnf fired (sync.Once gate).
	if len(d.bindings) != 3 {
		t.Fatalf("driver SetViewClickBinding calls = %d; want 3", len(d.bindings))
	}
	if len(w.msgs) != 1 {
		t.Fatalf("warn messages = %d; want 1 (warn-once gate)", len(w.msgs))
	}
}
