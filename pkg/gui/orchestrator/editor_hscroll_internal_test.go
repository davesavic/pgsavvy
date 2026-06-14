package orchestrator

import (
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"
)

// TestHorizontalOriginFor exercises the pure horizontal scroll-into-view
// math: given a caret column, the current origin, and the viewport width,
// return the origin that keeps the caret just inside the nearer edge
// (edge-anchored, like a text editor's horizontal scroll).
func TestHorizontalOriginFor(t *testing.T) {
	const width = 10
	cases := []struct {
		name        string
		col, origin int
		wantOrigin  int
	}{
		{"visible keeps origin", 5, 0, 0},
		{"first column", 0, 0, 0},
		{"last visible column unchanged", 9, 0, 0},
		{"one past right edge scrolls by one", 10, 0, 1},
		{"far right anchors caret to last column", 25, 0, 16},
		{"left of origin snaps origin to col", 3, 8, 3},
		{"col equal to origin is visible", 8, 8, 8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := horizontalOriginFor(tc.col, tc.origin, width)
			if got != tc.wantOrigin {
				t.Fatalf("horizontalOriginFor(%d, %d, %d) = %d, want %d",
					tc.col, tc.origin, width, got, tc.wantOrigin)
			}
			// The screen-relative caret column must stay inside the
			// viewport so gocui's ShowCursor guard (cx < InnerWidth)
			// keeps the caret visible.
			if cx := tc.col - got; cx < 0 || cx >= width {
				t.Fatalf("screen-relative cx = %d out of [0,%d)", cx, width)
			}
		})
	}
}

// TestScrollEditorColumnIntoView verifies the wiring against a real
// *gocui.View: a caret past the right edge pins the horizontal origin and
// rewrites the screen-relative cursor; scrolling back left then re-anchors
// the origin to the caret.
func TestScrollEditorColumnIntoView(t *testing.T) {
	// Width 12 => InnerWidth 10.
	v := gocui.NewView("qe", 0, 0, 11, 5, gocui.OutputNormal)

	scrollEditorColumnIntoView(v, 25)
	if ox := v.OriginX(); ox != 16 {
		t.Fatalf("after col 25: OriginX = %d, want 16", ox)
	}
	if cx := v.CursorX(); cx != 9 {
		t.Fatalf("after col 25: CursorX = %d, want 9", cx)
	}

	scrollEditorColumnIntoView(v, 3)
	if ox := v.OriginX(); ox != 3 {
		t.Fatalf("after col 3: OriginX = %d, want 3", ox)
	}
	if cx := v.CursorX(); cx != 0 {
		t.Fatalf("after col 3: CursorX = %d, want 0", cx)
	}
}
