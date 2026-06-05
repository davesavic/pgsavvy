package orchestrator

import (
	"strings"
	"testing"
)

// TestPromptPopupCols_FitsLongPath is the regression for dbsavvy-lcxe:
// a default export destination path (~64 chars) plus the "> " body
// prefix must fit inside the popup's inner width with at least one
// column to spare. gocui's draw() calls HideCursor() when the view
// cursor x >= inner width, so the end-of-buffer caret (at "> " + len)
// must land strictly inside. Before the fix the popup was fixed at 64
// cols, the path scrolled the view origin, the label/prefix were
// dragged off-screen, and the caret vanished.
func TestPromptPopupCols_FitsLongPath(t *testing.T) {
	path := strings.Repeat("a", 64)
	cols := promptPopupCols(len([]rune("Edit path")), len([]rune(path)), 130)

	inner := cols - 2               // frame borders consume 2 columns
	caretX := 2 + len([]rune(path)) // "> " prefix + cursor at end of buffer
	if caretX >= inner {
		t.Fatalf("caret x=%d not < inner width=%d (cols=%d): caret would be hidden", caretX, inner, cols)
	}
}

// TestPromptPopupCols_FloorAndClamp locks the floor (short prompts keep
// the compact promptMaxCols box) and the canvas clamp (the popup never
// grows past the available width).
func TestPromptPopupCols_FloorAndClamp(t *testing.T) {
	if got := promptPopupCols(5, 3, 130); got != promptMaxCols {
		t.Errorf("short content cols=%d, want floor %d", got, promptMaxCols)
	}
	if got := promptPopupCols(5, 500, 80); got != 80 {
		t.Errorf("over-wide content cols=%d, want canvas clamp 80", got)
	}
}
