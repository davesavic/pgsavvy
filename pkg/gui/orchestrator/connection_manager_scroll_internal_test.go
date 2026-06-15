package orchestrator

import "testing"

// TestApplyConnectionManagerScrollBringsFocusedFieldIntoView: the add/edit form
// bakes a "> " gutter onto the focused field row. When that row sits below the
// modal viewport (the field list overflows the 65%-height box) the layout
// scrolls the vertical origin just enough to reveal it.
func TestApplyConnectionManagerScrollBringsFocusedFieldIntoView(t *testing.T) {
	lines := make([]string, 25) // more fields than the box is tall
	for i := range lines {
		lines[i] = "  Field:"
	}
	lines[20] = "> Field:"
	v := buildPanelView(t, 10, true, lines) // InnerHeight 10, Wrap like the modal

	applyConnectionManagerScroll(v)

	// sel=20, height=10, oy starts 0 -> oy = 20-10+1 = 11.
	if got := v.OriginY(); got != 11 {
		t.Fatalf("OriginY = %d, want 11 (focused field scrolled into view)", got)
	}
}

// TestApplyConnectionManagerScrollFollowsFocusUp: moving focus back up to a row
// above the current origin re-anchors the origin onto it.
func TestApplyConnectionManagerScrollFollowsFocusUp(t *testing.T) {
	lines := make([]string, 25)
	for i := range lines {
		lines[i] = "  Field:"
	}
	lines[3] = "> Field:"
	v := buildPanelView(t, 10, true, lines)
	v.SetOriginY(11)

	applyConnectionManagerScroll(v)

	if got := v.OriginY(); got != 3 {
		t.Fatalf("OriginY = %d, want 3 (origin follows focus up)", got)
	}
}

// TestApplyConnectionManagerScrollNoMarkerLeavesOrigin: with no marker in the
// buffer (the connecting/empty body) the origin is left untouched.
func TestApplyConnectionManagerScrollNoMarkerLeavesOrigin(t *testing.T) {
	lines := make([]string, 25)
	for i := range lines {
		lines[i] = "  Field:"
	}
	v := buildPanelView(t, 10, true, lines)
	v.SetOriginY(7)

	applyConnectionManagerScroll(v)

	if got := v.OriginY(); got != 7 {
		t.Fatalf("OriginY = %d, want 7 (origin unchanged when no marker)", got)
	}
}

// TestApplyConnectionManagerScrollBringsSelectedRowIntoView: the list mode bakes
// the same "> " gutter onto the selected connection row, so a long connection
// list scrolls identically to the form.
func TestApplyConnectionManagerScrollBringsSelectedRowIntoView(t *testing.T) {
	lines := make([]string, 30) // more connections than the box is tall
	for i := range lines {
		lines[i] = "  conn"
	}
	lines[24] = "> conn"
	v := buildPanelView(t, 10, true, lines)

	applyConnectionManagerScroll(v)

	// sel=24, height=10 -> oy = 24-10+1 = 15.
	if got := v.OriginY(); got != 15 {
		t.Fatalf("OriginY = %d, want 15 (selected row scrolled into view)", got)
	}
}
