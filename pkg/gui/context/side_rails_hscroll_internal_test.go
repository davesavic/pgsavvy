package context

import (
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/mattn/go-runewidth"
)

// TestRailHorizontalOrigin exercises the pure rail horizontal scroll math:
// a row that fits the viewport keeps the origin at 0 (leading "> " marker
// anchored left); a row wider than the viewport anchors its END to the
// right edge so the truncated tail becomes readable.
func TestRailHorizontalOrigin(t *testing.T) {
	const inner = 10
	cases := []struct {
		name      string
		lineWidth int
		want      int
	}{
		{"fits keeps origin left", 6, 0},
		{"exact width keeps origin left", 10, 0},
		{"one past edge scrolls by one", 11, 1},
		{"far overflow anchors tail to edge", 29, 19},
		{"zero width origin left", 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := railHorizontalOrigin(tc.lineWidth, inner); got != tc.want {
				t.Fatalf("railHorizontalOrigin(%d, %d) = %d, want %d",
					tc.lineWidth, inner, got, tc.want)
			}
		})
	}

	// A non-positive viewport width must not panic and must pin to 0.
	if got := railHorizontalOrigin(50, 0); got != 0 {
		t.Fatalf("railHorizontalOrigin(50, 0) = %d, want 0", got)
	}
}

// TestFocusRailRow verifies the wiring against a real *gocui.View: selecting
// a row whose name overflows the pane scrolls the horizontal origin so the
// row's tail is visible; selecting a short row scrolls back to the left.
func TestFocusRailRow(t *testing.T) {
	// Width 12 => InnerWidth 10 (matches the editor hscroll test geometry).
	v := gocui.NewView("tables", 0, 0, 11, 5, gocui.OutputNormal)

	const longName = "a_very_long_table_name_that_overflows"
	longRow := "  " + longName
	v.SetContent("> users\n" + longRow + "\n")

	// Row 0 ("> users") fits within InnerWidth 10 => origin stays left.
	focusRailRow(v, 0)
	if ox := v.OriginX(); ox != 0 {
		t.Fatalf("short row: OriginX = %d, want 0", ox)
	}

	// Row 1 overflows => origin anchors the tail to the right edge.
	focusRailRow(v, 1)
	wantOX := runewidth.StringWidth(longRow) - v.InnerWidth()
	if ox := v.OriginX(); ox != wantOX {
		t.Fatalf("long row: OriginX = %d, want %d", ox, wantOX)
	}

	// Moving back to the short row re-anchors the origin to the left.
	focusRailRow(v, 0)
	if ox := v.OriginX(); ox != 0 {
		t.Fatalf("back to short row: OriginX = %d, want 0", ox)
	}
}
