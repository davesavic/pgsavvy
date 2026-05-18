package context

import (
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// fakeModeStore records Set/Reset calls; satisfies types.ModeSetter.
type fakeModeStore struct {
	set     map[types.ContextKey]types.Mode
	resets  []types.ContextKey
	sets    []types.ContextKey
}

func newFakeModeStore() *fakeModeStore {
	return &fakeModeStore{set: map[types.ContextKey]types.Mode{}}
}

func (f *fakeModeStore) Set(k types.ContextKey, m types.Mode) {
	f.set[k] = m
	f.sets = append(f.sets, k)
}

func (f *fakeModeStore) Reset(k types.ContextKey) {
	delete(f.set, k)
	f.resets = append(f.resets, k)
}

// cmdLineCaptureDriver mirrors the captureDriver shape from
// whichkey_context_test.go but lives here so the two tests stay
// independent (declaring it again rather than refactoring the existing
// shared stub).
type cmdLineCaptureDriver struct {
	cmdLineStubDriver
	lastView    string
	lastContent string
	writes      int
}

func (c *cmdLineCaptureDriver) Update(fn func() error) { _ = fn() }
func (c *cmdLineCaptureDriver) SetContent(view, str string) error {
	c.lastView = view
	c.lastContent = str
	c.writes++
	return nil
}

// cmdLineStubDriver supplies no-op implementations for every GuiDriver
// method captureDriver doesn't override.
type cmdLineStubDriver struct{}

func (cmdLineStubDriver) Write(string, []byte) (int, error) { return 0, nil }
func (cmdLineStubDriver) GetViewBuffer(string) string       { return "" }
func (cmdLineStubDriver) SetView(string, int, int, int, int, byte) (types.View, error) {
	return nil, nil
}
func (cmdLineStubDriver) SetKeybinding(string, types.Key, types.Modifier, func() error) error {
	return nil
}
func (cmdLineStubDriver) SetMasterEditor(string, gocui.Editor) error        { return nil }
func (cmdLineStubDriver) SetViewClickBinding(*types.ViewMouseBinding) error { return nil }
func (cmdLineStubDriver) UpdateContentOnly(fn func() error)                 { _ = fn() }
func (cmdLineStubDriver) SetCurrentView(string) (types.View, error)         { return nil, nil }
func (cmdLineStubDriver) SetViewOnTop(string) (types.View, error)           { return nil, nil }
func (cmdLineStubDriver) ViewByName(string) (types.View, error)             { return nil, nil }
func (cmdLineStubDriver) DeleteView(string) error                           { return nil }
func (cmdLineStubDriver) SetManager(...types.Manager)                       {}
func (cmdLineStubDriver) MainLoop() error                                   { return nil }
func (cmdLineStubDriver) Close() error                                      { return nil }

func newTestCommandLine(drv types.GuiDriver, modes types.ModeSetter) *CommandLineContext {
	base := NewBaseContext(BaseContextOpts{
		Key:      types.COMMAND_LINE,
		ViewName: string(types.COMMAND_LINE),
		Kind:     types.TEMPORARY_POPUP,
	})
	deps := types.ContextTreeDeps{GuiDriver: drv}
	return NewCommandLineContext(base, deps, modes)
}

func TestCommandLineContext_IdentityAndKind(t *testing.T) {
	c := newTestCommandLine(nil, nil)
	if c.GetKey() != types.COMMAND_LINE {
		t.Errorf("GetKey = %q, want %q", c.GetKey(), types.COMMAND_LINE)
	}
	if c.GetViewName() != string(types.COMMAND_LINE) {
		t.Errorf("GetViewName = %q, want %q", c.GetViewName(), string(types.COMMAND_LINE))
	}
	if c.GetKind() != types.TEMPORARY_POPUP {
		t.Errorf("GetKind = %v, want TEMPORARY_POPUP", c.GetKind())
	}
}

func TestCommandLineContext_HandleFocusSetsModeCommand(t *testing.T) {
	ms := newFakeModeStore()
	c := newTestCommandLine(nil, ms)
	if err := c.HandleFocus(types.OnFocusOpts{}); err != nil {
		t.Fatalf("HandleFocus: %v", err)
	}
	if got, ok := ms.set[types.COMMAND_LINE]; !ok || got != types.ModeCommand {
		t.Errorf("ModeStore[COMMAND_LINE] = %v ok=%v, want ModeCommand", got, ok)
	}
}

func TestCommandLineContext_HandleFocusLostResets(t *testing.T) {
	ms := newFakeModeStore()
	c := newTestCommandLine(nil, ms)
	c.SetBuffer("reload now")
	_ = c.HandleFocus(types.OnFocusOpts{})

	if err := c.HandleFocusLost(types.OnFocusLostOpts{}); err != nil {
		t.Fatalf("HandleFocusLost: %v", err)
	}
	if _, ok := ms.set[types.COMMAND_LINE]; ok {
		t.Errorf("ModeStore[COMMAND_LINE] still set after HandleFocusLost")
	}
	if c.Buffer() != "" {
		t.Errorf("Buffer = %q after HandleFocusLost, want empty", c.Buffer())
	}
}

func TestCommandLineContext_NilModesIsSafe(t *testing.T) {
	c := newTestCommandLine(nil, nil)
	if err := c.HandleFocus(types.OnFocusOpts{}); err != nil {
		t.Errorf("HandleFocus with nil modes: %v", err)
	}
	if err := c.HandleFocusLost(types.OnFocusLostOpts{}); err != nil {
		t.Errorf("HandleFocusLost with nil modes: %v", err)
	}
}

// HandleRender is a no-op for COMMAND_LINE post-lc2: the master
// gocui.Editor's Passthrough path writes user-typed runes into
// v.TextArea every keystroke and the orchestrator's Layout Tier-3
// pass prepopulates the leading ":" prompt on fresh view creation.
// HandleRender used to call SetContent here, but that was clearing
// v.lines after RenderTextArea on every frame — overwriting the
// typed text. The assertions below pin the no-op contract.
func TestCommandLineContext_HandleRenderIsNoOp(t *testing.T) {
	drv := &cmdLineCaptureDriver{}
	c := newTestCommandLine(drv, nil)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 0 {
		t.Fatalf("SetContent writes = %d, want 0 (HandleRender must not call SetContent)", drv.writes)
	}
}

func TestCommandLineContext_HandleRenderNilDriver(t *testing.T) {
	c := newTestCommandLine(nil, nil)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender with nil driver: %v", err)
	}
}

func TestCommandLineContext_BufferAccessors(t *testing.T) {
	c := newTestCommandLine(nil, nil)
	if got := c.Buffer(); got != "" {
		t.Errorf("Buffer initially = %q, want empty", got)
	}
	c.SetBuffer("foo bar")
	if got := c.Buffer(); got != "foo bar" {
		t.Errorf("Buffer = %q, want \"foo bar\"", got)
	}
	if got := c.ReadAndClearBuffer(); got != "foo bar" {
		t.Errorf("ReadAndClearBuffer = %q, want \"foo bar\"", got)
	}
	if got := c.Buffer(); got != "" {
		t.Errorf("Buffer after read = %q, want empty", got)
	}
}

func TestCommandLineContext_SatisfiesIBaseContext(t *testing.T) {
	var _ types.IBaseContext = &CommandLineContext{}
}
