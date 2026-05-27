package ui

import (
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
)

// stripBarStyle removes the reverse-video wrapper so a rendered strip can
// be measured / matched as plain text.
func stripBarStyle(s string) string {
	s = strings.ReplaceAll(s, ansiReverseSGR, "")
	s = strings.ReplaceAll(s, ansiResetSGR, "")
	return s
}

func TestRenderTabBar_EmptyAndZeroWidth(t *testing.T) {
	h, _ := newTestHelper(t, nil)
	if got := h.RenderTabBar(80); got != "" {
		t.Fatalf("no tabs: RenderTabBar = %q, want empty", got)
	}
	_ = h.openTab("q1", nil)
	if got := h.RenderTabBar(0); got != "" {
		t.Fatalf("width 0: RenderTabBar = %q, want empty", got)
	}
}

func TestRenderTabBar_ListsAllTabsAndHighlightsActive(t *testing.T) {
	h, _ := newTestHelper(t, nil)
	for _, sql := range []string{"users", "orders", "items"} {
		_ = h.openTab(sql, nil)
	}
	// Active is the most-recently opened (slot 2 -> "3 items").
	out := h.RenderTabBar(200)

	// All three tabs visible as "N label" cells.
	plain := stripBarStyle(out)
	for _, want := range []string{"1 users ", "2 orders ", "3 items "} {
		if !strings.Contains(plain, want) {
			t.Errorf("strip %q missing cell %q", plain, want)
		}
	}
	// Active cell is reverse-video wrapped around "3 items".
	if !strings.Contains(out, ansiReverseSGR+" 3 items ") {
		t.Errorf("active cell not reverse-wrapped in %q", out)
	}
	// Wide enough to avoid overflow markers.
	if strings.ContainsAny(out, "‹›") {
		t.Errorf("unexpected overflow markers in wide strip %q", out)
	}
}

func TestRenderTabBar_WindowsAroundActiveWhenNarrow(t *testing.T) {
	h, _ := newTestHelper(t, nil)
	for _, sql := range []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot"} {
		_ = h.openTab(sql, nil)
	}
	h.Jump(4) // activate slot 3 -> "4 delta"

	const width = 22
	out := h.RenderTabBar(width)
	plain := stripBarStyle(out)

	if !strings.Contains(out, ansiReverseSGR+" 4 delta ") {
		t.Errorf("active cell %q not visible/highlighted in narrow strip", "4 delta")
	}
	if !strings.ContainsAny(out, "‹›") {
		t.Errorf("expected overflow markers in narrow strip %q", out)
	}
	if w := runewidth.StringWidth(plain); w > width {
		t.Errorf("strip display width = %d, want <= %d (%q)", w, width, plain)
	}
}

func TestStateGlyph(t *testing.T) {
	cases := []struct {
		state TabState
		want  string
	}{
		{StateRunning, "▸"},
		{StateQueued, "…"},
		{StateComplete, "✓"},
		{StateCancelled, "⊘"},
		{StateDetached, "⇡"},
		{StatePlan, "⊞"},
		{StateErrored, "✗"}, // == StateError ("error")
	}
	for _, tc := range cases {
		state, want := tc.state, tc.want
		if got := stateGlyph(state); got != want {
			t.Errorf("stateGlyph(%q) = %q, want %q", state, got, want)
		}
	}
}

func TestWindowRange(t *testing.T) {
	tests := []struct {
		name               string
		widths             []int
		active, width      int
		wantStart, wantEnd int
	}{
		{"all fit", []int{5, 5, 5}, 1, 100, 0, 2},
		{"narrow keeps active only", []int{10, 10, 10}, 2, 12, 2, 2},
		{"single oversized active", []int{20}, 0, 5, 0, 0},
		{"grows right before left", []int{4, 4, 4, 4}, 1, 18, 1, 2},
	}
	const sepW = 3 // width of " │ "
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			start, end := windowRange(tc.widths, tc.active, tc.width, sepW)
			if start != tc.wantStart || end != tc.wantEnd {
				t.Errorf("windowRange = (%d,%d), want (%d,%d)", start, end, tc.wantStart, tc.wantEnd)
			}
			if start > tc.active || end < tc.active {
				t.Errorf("window (%d,%d) excludes active %d", start, end, tc.active)
			}
		})
	}
}
