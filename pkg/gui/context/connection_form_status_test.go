package context

import (
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/mattn/go-runewidth"
)

// maxLineWidth returns the widest physical line (in terminal columns) of a
// rendered body, ignoring trailing blank padding lines.
func maxLineWidth(body string) (int, string) {
	widest := 0
	var widestLine string
	for line := range strings.SplitSeq(body, "\n") {
		if w := runewidth.StringWidth(line); w > widest {
			widest, widestLine = w, line
		}
	}
	return widest, widestLine
}

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

// longErr mirrors the real password-prompt-refused failure surfaced by the
// test-connection action — long enough to wrap a typical modal width.
const longErr = "test failed: session: prompt: session: interactive password prompt not supported in TUI mode; configure password_command, keyring, or pgpass (hint: password)"

// TestConnectionForm_LongInlineMessageClippedToWidth is the regression for
// pgsavvy-xta7: with the inner width injected, a long inline err/status must be
// clipped so NO rendered line exceeds that width — otherwise the modal's
// Wrap=true reflows the message and corrupts the field rows below the focused
// field. The focused field is the top row, so a wrap would displace everything.
func TestConnectionForm_LongInlineMessageClippedToWidth(t *testing.T) {
	const innerWidth = 40
	drv := &formCaptureDriver{}
	c := newCaptureFormCtx(drv)
	c.OpenAddForm(nil, func() []string { return []string{"postgres"} })
	c.SetLabelWrapWidth(innerWidth)

	c.FormSetError(longErr)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	// No rendered line may exceed the inner width, else the modal's Wrap=true
	// reflows the message to multiple lines and displaces the rows below it
	// (pgsavvy-xta7).
	if w, line := maxLineWidth(drv.lastContent); w > innerWidth {
		t.Errorf("a rendered line is %d cols wide > innerWidth %d (would wrap and displace rows); line=%q\nbody=\n%s", w, innerWidth, line, drv.lastContent)
	}
	if !strings.Contains(drv.lastContent, "…") {
		t.Errorf("a clipped message must end with an ellipsis; body=\n%s", drv.lastContent)
	}
	// The leading, most-informative part of the error must survive the clip.
	if !strings.Contains(drv.lastContent, "test failed") {
		t.Errorf("clipped message dropped the informative prefix; body=\n%s", drv.lastContent)
	}
}

// TestConnectionForm_ShortMessageNotClipped asserts the clip is a no-op for a
// message that already fits: no spurious ellipsis, message intact.
func TestConnectionForm_ShortMessageNotClipped(t *testing.T) {
	drv := &formCaptureDriver{}
	c := newCaptureFormCtx(drv)
	c.OpenAddForm(nil, func() []string { return []string{"postgres"} })
	c.SetLabelWrapWidth(40)

	c.FormSetStatus("connection ok")
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if !strings.Contains(drv.lastContent, "connection ok") {
		t.Errorf("short status was altered; body=\n%s", drv.lastContent)
	}
	if strings.Contains(drv.lastContent, "…") {
		t.Errorf("short status got a spurious ellipsis; body=\n%s", drv.lastContent)
	}
}

// TestConnectionForm_NoClipWhenWidthUnknown asserts back-compat: with width 0
// (recorder/test path without injection) the message is left intact.
func TestConnectionForm_NoClipWhenWidthUnknown(t *testing.T) {
	drv := &formCaptureDriver{}
	c := newCaptureFormCtx(drv)
	c.OpenAddForm(nil, func() []string { return []string{"postgres"} })
	// no SetLabelWrapWidth call ⇒ msgWidth == 0

	c.FormSetError(longErr)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if !strings.Contains(drv.lastContent, longErr) {
		t.Errorf("width-0 must leave the message intact (back-compat); body=\n%s", drv.lastContent)
	}
}
