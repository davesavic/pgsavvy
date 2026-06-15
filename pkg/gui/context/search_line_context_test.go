package context

import (
	"strings"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// searchLineCaptureDriver records the last SetContent write so the render
// assertions can inspect the bottom line. Embeds the command-line stub
// driver (declared in command_line_context_test.go) for the rest of the
// GuiDriver surface.
type searchLineCaptureDriver struct {
	cmdLineStubDriver
	lastView    string
	lastContent string
	writes      int
}

func (c *searchLineCaptureDriver) Update(fn func() error) { _ = fn() }
func (c *searchLineCaptureDriver) SetContent(view, str string) error {
	c.lastView = view
	c.lastContent = str
	c.writes++
	return nil
}

func newTestSearchLine(drv types.GuiDriver, modes types.ModeSetter) *SearchLineContext {
	base := NewBaseContext(BaseContextOpts{
		Key:      types.SEARCH_LINE,
		ViewName: string(types.SEARCH_LINE),
		Kind:     types.TEMPORARY_POPUP,
	})
	deps := types.ContextTreeDeps{GuiDriver: drv}
	return NewSearchLineContext(base, deps, modes)
}

func TestSearchLineContext_IdentityAndKind(t *testing.T) {
	c := newTestSearchLine(nil, nil)
	if c.GetKey() != types.SEARCH_LINE {
		t.Errorf("GetKey = %q, want %q", c.GetKey(), types.SEARCH_LINE)
	}
	if c.GetViewName() != string(types.SEARCH_LINE) {
		t.Errorf("GetViewName = %q, want %q", c.GetViewName(), string(types.SEARCH_LINE))
	}
	if c.GetKind() != types.TEMPORARY_POPUP {
		t.Errorf("GetKind = %v, want TEMPORARY_POPUP", c.GetKind())
	}
}

func TestSearchLineContext_HandleFocusSetsModeCommand(t *testing.T) {
	ms := newFakeModeStore()
	c := newTestSearchLine(nil, ms)
	if err := c.HandleFocus(types.OnFocusOpts{}); err != nil {
		t.Fatalf("HandleFocus: %v", err)
	}
	if got, ok := ms.set[types.SEARCH_LINE]; !ok || got != types.ModeCommand {
		t.Errorf("ModeStore[SEARCH_LINE] = %v ok=%v, want ModeCommand", got, ok)
	}
}

func TestSearchLineContext_HandleFocusLostResets(t *testing.T) {
	ms := newFakeModeStore()
	c := newTestSearchLine(nil, ms)
	c.SetBuffer("abc")
	c.SetMatchCount("1/3")
	_ = c.HandleFocus(types.OnFocusOpts{})

	if err := c.HandleFocusLost(types.OnFocusLostOpts{}); err != nil {
		t.Fatalf("HandleFocusLost: %v", err)
	}
	if _, ok := ms.set[types.SEARCH_LINE]; ok {
		t.Errorf("ModeStore[SEARCH_LINE] still set after HandleFocusLost")
	}
	if c.Buffer() != "" {
		t.Errorf("Buffer = %q after HandleFocusLost, want empty", c.Buffer())
	}
}

// TestSearchLineContext_HandleRenderSingleLine pins the AC: a pushed
// SearchLine renders a single bottom line of "/" + query.
func TestSearchLineContext_HandleRenderSingleLine(t *testing.T) {
	drv := &searchLineCaptureDriver{}
	c := newTestSearchLine(drv, nil)
	c.SetBuffer("foo")
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.lastView != string(types.SEARCH_LINE) {
		t.Errorf("SetContent view = %q, want %q", drv.lastView, string(types.SEARCH_LINE))
	}
	if drv.lastContent != "/foo" {
		t.Errorf("SetContent = %q, want %q", drv.lastContent, "/foo")
	}
	if strings.Contains(drv.lastContent, "\n") {
		t.Errorf("SetContent has newline %q, want single line", drv.lastContent)
	}
}

// Empty query still renders just the "/" prefix.
func TestSearchLineContext_HandleRenderEmptyQuery(t *testing.T) {
	drv := &searchLineCaptureDriver{}
	c := newTestSearchLine(drv, nil)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.lastContent != "/" {
		t.Errorf("SetContent = %q, want %q", drv.lastContent, "/")
	}
}

// With a width + match count the count slot is right-aligned.
func TestSearchLineContext_HandleRenderRightAlignsCount(t *testing.T) {
	drv := &searchLineCaptureDriver{}
	c := newTestSearchLine(drv, nil)
	c.SetBuffer("foo")
	c.SetWidth(10)
	c.SetMatchCount("1/3")
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	// "/foo" (4) + pad (3) + "1/3" (3) = 10 columns.
	want := "/foo   1/3"
	if drv.lastContent != want {
		t.Errorf("SetContent = %q, want %q", drv.lastContent, want)
	}
}

// TestSearchLineContext_PassthroughTextArea drives a printable rune
// through the master-editor Passthrough mechanism into the SEARCH_LINE
// TextArea (the runtime source of truth), then confirms Buffer() reflects
// it via the plumbed view.
func TestSearchLineContext_PassthroughTextArea(t *testing.T) {
	c := newTestSearchLine(nil, nil)
	v := gocui.NewView(string(types.SEARCH_LINE), 0, 0, 20, 2, gocui.OutputNormal)
	c.SetView(v)
	// Simulate the Passthrough write gocui.DefaultEditor performs.
	v.TextArea.TypeCharacter("a")
	v.TextArea.TypeCharacter("b")
	if got := c.Buffer(); got != "ab" {
		t.Errorf("Buffer = %q, want %q", got, "ab")
	}
	if got := c.ReadAndClearBuffer(); got != "ab" {
		t.Errorf("ReadAndClearBuffer = %q, want %q", got, "ab")
	}
	if got := c.Buffer(); got != "" {
		t.Errorf("Buffer after clear = %q, want empty", got)
	}
}

func TestSearchLineContext_SatisfiesIBaseContext(t *testing.T) {
	var _ types.IBaseContext = &SearchLineContext{}
}
