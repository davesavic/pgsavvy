package context

import "testing"

// With no visibility filter every row paints, so the rendered buffer-line
// index equals the raw cursor index.
func TestRenderedCursorRow_NoFilterIsIdentity(t *testing.T) {
	c := newRailCtx(t, "a", "b", "c", "d")
	c.SetCursor(2)
	if got := c.RenderedCursorRow(); got != 2 {
		t.Fatalf("RenderedCursorRow() = %d, want 2 (identity without filter)", got)
	}
}

// The skew case: rows 0 and 1 are hidden, so the row at raw index 3 paints on
// buffer line 1. A lookup keyed by the raw cursor (3) would address the wrong
// or out-of-range line — RenderedCursorRow must compact to 1.
func TestRenderedCursorRow_SkipsHiddenRowsBeforeCursor(t *testing.T) {
	c := newRailCtx(t, "app", "public", "reporting", "very_long_name")
	c.SetRailVisible(func(i int) bool { return i >= 2 }) // hide app, public
	c.SetCursor(3)
	if got := c.RenderedCursorRow(); got != 1 {
		t.Fatalf("RenderedCursorRow() = %d, want 1 (two earlier rows hidden)", got)
	}
}

// Hidden rows after the cursor don't shift the rendered position.
func TestRenderedCursorRow_IgnoresHiddenRowsAfterCursor(t *testing.T) {
	c := newRailCtx(t, "a", "b", "c", "d")
	c.SetRailVisible(func(i int) bool { return i != 3 }) // hide last
	c.SetCursor(1)
	if got := c.RenderedCursorRow(); got != 1 {
		t.Fatalf("RenderedCursorRow() = %d, want 1", got)
	}
}

// A cursor parked on a hidden row paints no marker, so there is no rendered
// selection to address — callers treat -1 as "no row" and skip scrolling.
func TestRenderedCursorRow_CursorOnHiddenRowIsNegative(t *testing.T) {
	c := newRailCtx(t, "a", "b", "c")
	c.SetRailVisible(func(i int) bool { return i != 1 })
	c.SetCursor(1)
	if got := c.RenderedCursorRow(); got != -1 {
		t.Fatalf("RenderedCursorRow() = %d, want -1 (cursor on hidden row)", got)
	}
}
