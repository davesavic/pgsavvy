package orchestrator

import (
	"strings"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"
)

// buildPanelView returns a real *gocui.View sized to innerH writable rows
// (Height = innerH+2 for the frame) populated with the given lines.
func buildPanelView(t *testing.T, innerH int, wrap bool, lines []string) *gocui.View {
	t.Helper()
	v := gocui.NewView("relationship_panel", 0, 0, 40, innerH+1, gocui.OutputNormal)
	v.Wrap = wrap
	v.SetContent(strings.Join(lines, "\n"))
	if got := v.InnerHeight(); got != innerH {
		t.Fatalf("InnerHeight = %d, want %d", got, innerH)
	}
	return v
}

// TestApplyRelationshipPanelScrollBringsSelectionIntoView: a "> " marker below
// the viewport scrolls the vertical origin just enough to show it.
func TestApplyRelationshipPanelScrollBringsSelectionIntoView(t *testing.T) {
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = "  row"
	}
	lines[15] = "> selected"
	v := buildPanelView(t, 5, false, lines) // InnerHeight 5

	applyRelationshipPanelScroll(v, true)

	// sel=15, height=5, oy starts 0 -> oy = 15-5+1 = 11.
	if got := v.OriginY(); got != 11 {
		t.Fatalf("OriginY = %d, want 11 (selection scrolled into view)", got)
	}
}

// TestApplyRelationshipPanelScrollSelectionAboveOrigin: scrolling back up to a
// marker above the current origin re-anchors the origin to it.
func TestApplyRelationshipPanelScrollSelectionAboveOrigin(t *testing.T) {
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = "  row"
	}
	lines[2] = "> selected"
	v := buildPanelView(t, 5, false, lines)
	v.SetOriginY(11)

	applyRelationshipPanelScroll(v, true)

	if got := v.OriginY(); got != 2 {
		t.Fatalf("OriginY = %d, want 2 (origin follows selection up)", got)
	}
}

// TestApplyRelationshipPanelScrollNotFocusedPinsTop: follow mode always shows
// the panel from the top regardless of any prior scroll.
func TestApplyRelationshipPanelScrollNotFocusedPinsTop(t *testing.T) {
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = "  row"
	}
	v := buildPanelView(t, 5, false, lines)
	v.SetOriginY(9)

	applyRelationshipPanelScroll(v, false)

	if got := v.OriginY(); got != 0 {
		t.Fatalf("OriginY = %d, want 0 (follow mode pins to top)", got)
	}
}

// TestApplyRelationshipPanelScrollWrapUsesVisualLines: with Wrap on, a line
// wider than the panel occupies two visual rows; the origin must be computed in
// visual-line space so the marker below it stays on screen.
func TestApplyRelationshipPanelScrollWrapUsesVisualLines(t *testing.T) {
	// InnerWidth = 40-2 = 38. A 60-char line wraps to 2 visual rows.
	wide := strings.Repeat("x", 60)
	lines := []string{wide, wide, wide, wide, "  a", "  b", "> selected"}
	v := buildPanelView(t, 3, true, lines) // InnerHeight 3

	applyRelationshipPanelScroll(v, true)

	// 4 wide lines -> 8 visual rows, then "  a","  b","> selected" at visual
	// indices 8,9,10. height=3 -> oy = 10-3+1 = 8.
	if got := v.OriginY(); got != 8 {
		t.Fatalf("OriginY = %d, want 8 (visual-line aware with wrap)", got)
	}
}
