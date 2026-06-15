package context

import (
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// formCaptureDriver records the last SetContent body so the inline
// status/error render can be asserted. It embeds cmdLineStubDriver for the
// GuiDriver methods this test does not exercise.
type formCaptureDriver struct {
	cmdLineStubDriver
	lastContent string
}

func (d *formCaptureDriver) Update(fn func() error)            { _ = fn() }
func (d *formCaptureDriver) UpdateContentOnly(fn func() error) { _ = fn() }
func (d *formCaptureDriver) SetContent(_, str string) error {
	d.lastContent = str
	return nil
}

func newCaptureFormCtx(drv types.GuiDriver) *ConnectionManagerContext {
	base := NewBaseContext(BaseContextOpts{
		Key:      types.CONNECTION_MANAGER,
		ViewName: string(types.CONNECTION_MANAGER),
		Kind:     types.MAIN_CONTEXT,
	})
	return NewConnectionManagerContext(base, types.ContextTreeDeps{GuiDriver: drv})
}

// TestConnectionForm_StatusRendersInline asserts FormSetStatus stamps a line
// rendered under the focused field in the form body (inline PASS surface).
func TestConnectionForm_StatusRendersInline(t *testing.T) {
	drv := &formCaptureDriver{}
	c := newCaptureFormCtx(drv)
	c.OpenAddForm(nil, func() []string { return []string{"postgres"} })

	c.FormSetStatus("connection ok")
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if !strings.Contains(drv.lastContent, "connection ok") {
		t.Errorf("status line not rendered inline; body=\n%s", drv.lastContent)
	}
}

// TestConnectionForm_ErrorRendersInline asserts FormSetError stamps an inline
// error line (inline FAIL surface), and that setting an error clears a prior
// status (they are mutually exclusive).
func TestConnectionForm_ErrorRendersInline(t *testing.T) {
	drv := &formCaptureDriver{}
	c := newCaptureFormCtx(drv)
	c.OpenAddForm(nil, func() []string { return []string{"postgres"} })

	c.FormSetStatus("connection ok")
	c.FormSetError("test failed: dial error")
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if !strings.Contains(drv.lastContent, "test failed") {
		t.Errorf("error line not rendered inline; body=\n%s", drv.lastContent)
	}
	if strings.Contains(drv.lastContent, "connection ok") {
		t.Errorf("stale status line survived an error stamp; body=\n%s", drv.lastContent)
	}
}

// TestConnectionForm_StatusClearsOnFieldEdit asserts editing a field clears a
// prior status line so a stale pass/fail does not linger after the form changes.
func TestConnectionForm_StatusClearsOnFieldEdit(t *testing.T) {
	drv := &formCaptureDriver{}
	c := newCaptureFormCtx(drv)
	c.OpenAddForm(nil, func() []string { return []string{"postgres"} })

	c.FormSetStatus("connection ok")
	c.FormSetFocusedValue("renamed") // edits the focused (name) field
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if strings.Contains(drv.lastContent, "connection ok") {
		t.Errorf("status line survived a field edit; body=\n%s", drv.lastContent)
	}
}
