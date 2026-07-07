package context

import (
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

func newTestChangelog(drv types.GuiDriver, releaseNotes string) *ChangelogContext {
	base := NewBaseContext(BaseContextOpts{
		Key:      types.CHANGELOG,
		ViewName: string(types.CHANGELOG),
		Kind:     types.PERSISTENT_POPUP,
	})
	deps := types.ContextTreeDeps{GuiDriver: drv, ReleaseNotesContent: releaseNotes}
	return NewChangelogContext(base, deps)
}

func TestChangelogContext_HasCorrectKeyAndKind(t *testing.T) {
	tree := NewContextTree(types.ContextTreeDeps{})
	if tree.Changelog == nil {
		t.Fatal("Changelog field is nil after NewContextTree")
	}
	if tree.Changelog.GetKey() != types.CHANGELOG {
		t.Fatalf("Changelog.GetKey() = %q, want CHANGELOG", tree.Changelog.GetKey())
	}
	if tree.Changelog.GetKind() != types.PERSISTENT_POPUP {
		t.Fatalf("Changelog.GetKind() = %d, want PERSISTENT_POPUP", tree.Changelog.GetKind())
	}
}

func TestChangelogContext_NeedsRerenderOnWidthChange(t *testing.T) {
	tree := NewContextTree(types.ContextTreeDeps{})
	if !tree.Changelog.NeedsRerenderOnWidthChange() {
		t.Error("Changelog must request re-render on width change")
	}
}

func TestChangelogContext_RenderInactive(t *testing.T) {
	drv := &captureDriver{}
	c := newTestChangelog(drv, "test content")
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender() inactive: %v", err)
	}
	if drv.writes != 0 {
		t.Error("HandleRender must not write when inactive")
	}
}

func TestChangelogContext_RenderActive(t *testing.T) {
	drv := &captureDriver{}
	content := "pgsavvy v1.0.0\n\nInitial release."
	c := newTestChangelog(drv, content)
	c.Open("v1.0.0")
	if !c.Active() {
		t.Fatal("Changelog must be active after Open")
	}
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender() active: %v", err)
	}
	expected := content
	if drv.lastContent != expected {
		t.Fatalf("content mismatch:\n got  %q\n want %q", drv.lastContent, expected)
	}
}

func TestChangelogContext_RenderActiveEmptyContent(t *testing.T) {
	drv := &captureDriver{}
	c := newTestChangelog(drv, "")
	c.Open("v1.0.0")
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender() active empty: %v", err)
	}
	if drv.writes != 1 {
		t.Fatalf("expected 1 write, got %d", drv.writes)
	}
	if drv.lastContent != "" {
		t.Fatalf("expected empty content for empty release notes, got: %q", drv.lastContent)
	}
}

func TestChangelogContext_NilGuiDriverNoPanic(t *testing.T) {
	c := newTestChangelog(nil, "test")
	c.Open("v1.0.0")
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender with nil driver: %v", err)
	}
}

func TestChangelogContext_ScrollClampNegative(t *testing.T) {
	c := newTestChangelog(&captureDriver{}, "")
	c.SetScrollY(5)
	if c.ScrollY() != 5 {
		t.Fatalf("ScrollY = %d, want 5", c.ScrollY())
	}
	c.SetScrollY(-3)
	if c.ScrollY() != 0 {
		t.Fatalf("ScrollY after negative = %d, want 0", c.ScrollY())
	}
}

func TestChangelogContext_ScrollDown(t *testing.T) {
	c := newTestChangelog(&captureDriver{}, "")
	c.Scroll(1)
	if c.ScrollY() != 1 {
		t.Fatalf("ScrollY after Scroll(1) = %d, want 1", c.ScrollY())
	}
}

func TestChangelogContext_ScrollUp(t *testing.T) {
	c := newTestChangelog(&captureDriver{}, "")
	c.SetScrollY(5)
	c.Scroll(-1)
	if c.ScrollY() != 4 {
		t.Fatalf("ScrollY after Scroll(-1) = %d, want 4", c.ScrollY())
	}
}

func TestChangelogContext_TotalWrappedLines(t *testing.T) {
	drv := &captureDriver{}
	c := newTestChangelog(drv, "line 1\nline 2")
	c.Open("v1.0.0")
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender(): %v", err)
	}
	if c.TotalWrappedLines() != 2 {
		t.Fatalf("TotalWrappedLines = %d, want 2", c.TotalWrappedLines())
	}
}

func TestChangelogContext_HandleFocusAndLost(t *testing.T) {
	c := newTestChangelog(&captureDriver{}, "")
	if c.Active() {
		t.Fatal("Changelog must not be active at construction")
	}
	if err := c.HandleFocus(types.OnFocusOpts{}); err != nil {
		t.Fatalf("HandleFocus: %v", err)
	}
	if !c.Active() {
		t.Fatal("Changelog must be active after HandleFocus")
	}
	if err := c.HandleFocusLost(types.OnFocusLostOpts{}); err != nil {
		t.Fatalf("HandleFocusLost: %v", err)
	}
	if c.Active() {
		t.Fatal("Changelog must be inactive after HandleFocusLost")
	}
}

func TestChangelogContext_SafeTextApplied(t *testing.T) {
	drv := &captureDriver{}
	c := newTestChangelog(drv, "safe\x1b[2Jtext")
	c.Open("v1.0.0")
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender(): %v", err)
	}
	if strings.Contains(drv.lastContent, "\x1b") {
		t.Fatal("content must not contain raw ESC bytes after SafeText")
	}
}

func TestChangelogContext_Close(t *testing.T) {
	c := newTestChangelog(&captureDriver{}, "")
	c.Open("v1.0.0")
	c.Close()
	if c.Active() {
		t.Fatal("Changelog must be inactive after Close")
	}
	if c.TotalWrappedLines() != 0 {
		t.Fatalf("TotalWrappedLines after close = %d, want 0", c.TotalWrappedLines())
	}
}
