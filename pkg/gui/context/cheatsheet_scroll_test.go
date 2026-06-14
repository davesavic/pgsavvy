package context

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/popup"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

func newTestCheatsheet() *CheatsheetContext {
	base := NewBaseContext(BaseContextOpts{
		Key:      types.CHEATSHEET,
		ViewName: string(types.CHEATSHEET),
		Kind:     types.DISPLAY_CONTEXT,
	})
	return NewCheatsheetContext(base, types.ContextTreeDeps{}, nil)
}

// TestCheatsheetScroll asserts the scroll offset accumulates, clamps at
// the top (never negative), and is reset when a fresh TabbedPopup state
// is installed (each `?` press / tab cycle starts at the top).
func TestCheatsheetScroll(t *testing.T) {
	c := newTestCheatsheet()

	if got := c.ScrollY(); got != 0 {
		t.Fatalf("fresh ScrollY() = %d, want 0", got)
	}

	c.Scroll(3)
	c.Scroll(2)
	if got := c.ScrollY(); got != 5 {
		t.Fatalf("after Scroll(+3,+2) ScrollY() = %d, want 5", got)
	}

	c.Scroll(-100)
	if got := c.ScrollY(); got != 0 {
		t.Fatalf("Scroll past top ScrollY() = %d, want 0 (clamped)", got)
	}

	c.SetScrollY(7)
	if got := c.ScrollY(); got != 7 {
		t.Fatalf("SetScrollY(7) ScrollY() = %d, want 7", got)
	}
	c.SetScrollY(-1)
	if got := c.ScrollY(); got != 0 {
		t.Fatalf("SetScrollY(-1) ScrollY() = %d, want 0 (clamped)", got)
	}

	c.SetScrollY(9)
	c.SetState(popup.NewTabbedPopup(nil))
	if got := c.ScrollY(); got != 0 {
		t.Fatalf("after SetState ScrollY() = %d, want 0 (reset)", got)
	}
}
