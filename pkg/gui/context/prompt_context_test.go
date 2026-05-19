package context

import (
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// fakePromptState bridges the (Active, Label) live on ui.PromptHelper
// with the Buffer that lives on controllers.PromptController. The test
// flattens both into one struct.
type fakePromptState struct {
	active bool
	label  string
	buffer string
}

func (f *fakePromptState) Active() bool   { return f.active }
func (f *fakePromptState) Label() string  { return f.label }
func (f *fakePromptState) Buffer() string { return f.buffer }

func newTestPrompt(state PromptState, drv types.GuiDriver) *PromptContext {
	base := NewBaseContext(BaseContextOpts{
		Key:      types.PROMPT,
		ViewName: string(types.PROMPT),
		Kind:     types.TEMPORARY_POPUP,
	})
	deps := types.ContextTreeDeps{GuiDriver: drv}
	c := NewPromptContext(base, deps)
	if state != nil {
		c.SetState(state)
	}
	return c
}

func TestPromptContext_NilStateNoOps(t *testing.T) {
	drv := &captureDriver{}
	c := newTestPrompt(nil, drv)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 0 {
		t.Errorf("SetContent called %d times with nil state; want 0", drv.writes)
	}
}

func TestPromptContext_InactiveNoOps(t *testing.T) {
	drv := &captureDriver{}
	state := &fakePromptState{active: false, label: "Connection name", buffer: "alice"}
	c := newTestPrompt(state, drv)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 0 {
		t.Errorf("SetContent called %d times when inactive; want 0", drv.writes)
	}
}

func TestPromptContext_RendersLabelAndBuffer(t *testing.T) {
	drv := &captureDriver{}
	state := &fakePromptState{active: true, label: "Connection name", buffer: "alice"}
	c := newTestPrompt(state, drv)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 1 {
		t.Fatalf("writes = %d, want 1", drv.writes)
	}
	if drv.lastView != string(types.PROMPT) {
		t.Errorf("view = %q, want %q", drv.lastView, string(types.PROMPT))
	}
	body := drv.lastContent
	if !strings.Contains(body, "Connection name") {
		t.Errorf("body missing label; got %q", body)
	}
	if !strings.Contains(body, "alice") {
		t.Errorf("body missing buffer; got %q", body)
	}
}

func TestPromptContext_RendersEmptyBuffer(t *testing.T) {
	drv := &captureDriver{}
	state := &fakePromptState{active: true, label: "Name", buffer: ""}
	c := newTestPrompt(state, drv)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 1 {
		t.Fatalf("writes = %d, want 1", drv.writes)
	}
	if !strings.Contains(drv.lastContent, "Name") {
		t.Errorf("body missing label with empty buffer; got %q", drv.lastContent)
	}
}

func TestPromptContext_NilGuiDriverNoPanic(t *testing.T) {
	state := &fakePromptState{active: true, label: "x", buffer: "y"}
	c := newTestPrompt(state, nil)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender with nil driver: %v", err)
	}
}

// TestPromptContext_CursorXY_ActiveBuffer asserts the caret coordinates
// match the position the layout pass should SetViewCursor to: the "> "
// prefix is 2 chars on body line 2 (label=0, blank=1, "> <buf>"=2), so
// x = 2 + len(buffer), y = 2. ok=true only when state is active.
func TestPromptContext_CursorXY_ActiveBuffer(t *testing.T) {
	state := &fakePromptState{active: true, label: "Connection name", buffer: "alice"}
	c := newTestPrompt(state, &captureDriver{})
	x, y, ok := c.CursorXY()
	if !ok {
		t.Fatal("CursorXY ok=false for active state; want true")
	}
	if wantX := 2 + len("alice"); x != wantX {
		t.Errorf("x = %d, want %d", x, wantX)
	}
	if y != 2 {
		t.Errorf("y = %d, want 2", y)
	}
}

func TestPromptContext_CursorXY_EmptyBuffer(t *testing.T) {
	state := &fakePromptState{active: true, label: "Name", buffer: ""}
	c := newTestPrompt(state, &captureDriver{})
	x, y, ok := c.CursorXY()
	if !ok {
		t.Fatal("CursorXY ok=false for active state with empty buffer; want true")
	}
	if x != 2 {
		t.Errorf("x = %d, want 2 (just past '> ')", x)
	}
	if y != 2 {
		t.Errorf("y = %d, want 2", y)
	}
}

func TestPromptContext_CursorXY_Inactive(t *testing.T) {
	state := &fakePromptState{active: false, label: "x", buffer: "y"}
	c := newTestPrompt(state, &captureDriver{})
	if _, _, ok := c.CursorXY(); ok {
		t.Error("CursorXY ok=true while inactive; want false")
	}
}

func TestPromptContext_CursorXY_NilState(t *testing.T) {
	c := newTestPrompt(nil, &captureDriver{})
	if _, _, ok := c.CursorXY(); ok {
		t.Error("CursorXY ok=true with nil state; want false")
	}
}
