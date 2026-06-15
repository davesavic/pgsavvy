package orchestrator

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/gui/editor"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// TestPopupRectFor_TableInspect locks in the 60% × 60% rect for the
// TABLE_INSPECT popup. Uses a 100×100
// popup-overlay canvas so 0.6 fractions land at 60 in either axis.
func TestPopupRectFor_TableInspect(t *testing.T) {
	canvas := ui.Dimensions{X0: 0, Y0: 0, X1: 100, Y1: 100}
	dims := map[string]ui.Dimensions{"popup-overlay": canvas}

	r, ok := popupRectFor(types.TABLE_INSPECT, dims, 100, 100)
	if !ok {
		t.Fatalf("popupRectFor(TABLE_INSPECT) ok=false, want true")
	}
	if w := r.X1 - r.X0; w < 55 || w > 65 {
		t.Errorf("width %d not ~60", w)
	}
	if h := r.Y1 - r.Y0; h < 55 || h > 65 {
		t.Errorf("height %d not ~60", h)
	}
}

// TestPopupRectFor_CellEditor locks in the small, height-bounded rect
// for the CELL_EDITOR popup. The core property is the
// bounded height (~3 content rows + borders) so the popup does not
// occlude the result grid; width is ~60% of the canvas. Uses a 100×100
// popup-overlay canvas.
func TestPopupRectFor_CellEditor(t *testing.T) {
	canvas := ui.Dimensions{X0: 0, Y0: 0, X1: 100, Y1: 100}
	dims := map[string]ui.Dimensions{"popup-overlay": canvas}

	r, ok := popupRectFor(types.CELL_EDITOR, dims, 100, 100)
	if !ok {
		t.Fatalf("popupRectFor(CELL_EDITOR) ok=false, want true")
	}
	if h := r.Y1 - r.Y0; h > 6 {
		t.Errorf("height %d not height-bounded (want <= 6)", h)
	}
	if w := r.X1 - r.X0; w < 50 || w > 70 {
		t.Errorf("width %d not ~60%% of canvas", w)
	}

	// No "popup-overlay" entry: bail out cleanly (no panic), matching
	// the default branch.
	if r2, ok2 := popupRectFor(types.CELL_EDITOR, map[string]ui.Dimensions{}, 100, 100); ok2 || r2 != (rect{}) {
		t.Errorf("missing popup-overlay: got (%v, %v), want (rect{}, false)", r2, ok2)
	}
}

// TestPopupRectFor_CommitDialog locks in a centered rect for the
// COMMIT_DIALOG popup. Without a popupRectFor case the
// dialog was pushed onto the focus stack but never got a view, so it
// rendered blank and never received input focus. Width is ~70% of the
// canvas (room for SQL preview lines), height ~60%. Uses a 100×100
// popup-overlay canvas.
func TestPopupRectFor_CommitDialog(t *testing.T) {
	canvas := ui.Dimensions{X0: 0, Y0: 0, X1: 100, Y1: 100}
	dims := map[string]ui.Dimensions{"popup-overlay": canvas}

	r, ok := popupRectFor(types.COMMIT_DIALOG, dims, 100, 100)
	if !ok {
		t.Fatalf("popupRectFor(COMMIT_DIALOG) ok=false, want true")
	}
	if w := r.X1 - r.X0; w < 65 || w > 75 {
		t.Errorf("width %d not ~70%% of canvas", w)
	}
	if h := r.Y1 - r.Y0; h < 55 || h > 65 {
		t.Errorf("height %d not ~60%% of canvas", h)
	}

	// No "popup-overlay" entry: bail out cleanly (no panic), matching
	// the default branch.
	if r2, ok2 := popupRectFor(types.COMMIT_DIALOG, map[string]ui.Dimensions{}, 100, 100); ok2 || r2 != (rect{}) {
		t.Errorf("missing popup-overlay: got (%v, %v), want (rect{}, false)", r2, ok2)
	}
}

// TestPopupRectFor_ConflictDialog locks in a centered 60% × 60% rect for
// the CONFLICT_DIALOG popup (same root cause as the COMMIT_DIALOG).
// Without a popupRectFor case the dialog was pushed onto
// the focus stack but never got a view, so it rendered blank and never
// received input focus. Uses a 100×100 popup-overlay canvas so 0.6
// fractions land at 60 in either axis.
func TestPopupRectFor_ConflictDialog(t *testing.T) {
	canvas := ui.Dimensions{X0: 0, Y0: 0, X1: 100, Y1: 100}
	dims := map[string]ui.Dimensions{"popup-overlay": canvas}

	r, ok := popupRectFor(types.CONFLICT_DIALOG, dims, 100, 100)
	if !ok {
		t.Fatalf("popupRectFor(CONFLICT_DIALOG) ok=false, want true")
	}
	if w := r.X1 - r.X0; w < 55 || w > 65 {
		t.Errorf("width %d not ~60%% of canvas", w)
	}
	if h := r.Y1 - r.Y0; h < 55 || h > 65 {
		t.Errorf("height %d not ~60%% of canvas", h)
	}

	// No "popup-overlay" entry: bail out cleanly (no panic), matching
	// the default branch.
	if r2, ok2 := popupRectFor(types.CONFLICT_DIALOG, map[string]ui.Dimensions{}, 100, 100); ok2 || r2 != (rect{}) {
		t.Errorf("missing popup-overlay: got (%v, %v), want (rect{}, false)", r2, ok2)
	}
}

// TestPopupRectFor_FKReversePicker locks in a centered 60% × 60% rect for
// the FK_REVERSE_PICKER popup (same root cause as the COMMIT_DIALOG).
// Without a popupRectFor case the picker was pushed onto the
// focus stack but never got a view, so it rendered blank and never
// received input focus. Uses a 100×100 popup-overlay canvas so 0.6
// fractions land at 60 in either axis.
func TestPopupRectFor_FKReversePicker(t *testing.T) {
	canvas := ui.Dimensions{X0: 0, Y0: 0, X1: 100, Y1: 100}
	dims := map[string]ui.Dimensions{"popup-overlay": canvas}

	r, ok := popupRectFor(types.FK_REVERSE_PICKER, dims, 100, 100)
	if !ok {
		t.Fatalf("popupRectFor(FK_REVERSE_PICKER) ok=false, want true")
	}
	if w := r.X1 - r.X0; w < 55 || w > 65 {
		t.Errorf("width %d not ~60%% of canvas", w)
	}
	if h := r.Y1 - r.Y0; h < 55 || h > 65 {
		t.Errorf("height %d not ~60%% of canvas", h)
	}

	// No "popup-overlay" entry: bail out cleanly (no panic), matching
	// the default branch.
	if r2, ok2 := popupRectFor(types.FK_REVERSE_PICKER, map[string]ui.Dimensions{}, 100, 100); ok2 || r2 != (rect{}) {
		t.Errorf("missing popup-overlay: got (%v, %v), want (rect{}, false)", r2, ok2)
	}
}

// TestPopupRectAnchoredBelowCursor: the SUGGESTIONS popup
// is cursor-anchored. anchoredRect derives the screen cell directly below
// the cursor from the editor view origin (vx0,vy0), the scroll offset
// (ox,oy) and the rune-indexed anchor (Line,Col): screen_x = vx0 + 1 +
// (Col-ox), screen_y = vy0 + 1 + (Line-oy) + 1. The +1 offsets account
// for the gocui frame border (content starts one cell inside the view).
// With no scroll, the top-left sits one row below the cursor, not
// screen-center.
func TestPopupRectAnchoredBelowCursor(t *testing.T) {
	// Editor view occupies (5,3)-(105,33). Cursor mid-view at Line 10,
	// Col 8, no scroll. 3 suggestions, content width 12.
	r := anchoredRect(5, 3, 105, 33, 0, 0, editor.Position{Line: 10, Col: 8}, 12, 3)

	wantX0 := 5 + 1 + 8      // vx0 + 1 (frame) + (Col - ox)
	wantY0 := 3 + 1 + 10 + 1 // vy0 + 1 (frame) + (Line - oy) + 1 (below cursor)
	if r.X0 != wantX0 {
		t.Errorf("X0 = %d, want %d (below-cursor column)", r.X0, wantX0)
	}
	if r.Y0 != wantY0 {
		t.Errorf("Y0 = %d, want %d (row below cursor)", r.Y0, wantY0)
	}
	// Not screen-centered: center of a 100-wide view would be ~55.
	if r.X0 > 40 {
		t.Errorf("X0 = %d looks screen-centered, want anchored", r.X0)
	}
}

// TestPopupRectAnchoredScrollOffset (edge): when the editor
// is scrolled (oy>0), screen_y subtracts the scroll origin so the popup
// tracks the on-screen cursor, not the buffer line.
func TestPopupRectAnchoredScrollOffset(t *testing.T) {
	// oy=6: buffer Line 10 is screen row 4 inside the view.
	r := anchoredRect(5, 3, 105, 33, 0, 6, editor.Position{Line: 10, Col: 8}, 12, 3)
	wantY0 := 3 + 1 + (10 - 6) + 1
	if r.Y0 != wantY0 {
		t.Errorf("Y0 = %d, want %d (scroll-adjusted row below cursor)", r.Y0, wantY0)
	}
	// ox=2: buffer Col 8 is screen col 6.
	r2 := anchoredRect(5, 3, 105, 33, 2, 0, editor.Position{Line: 10, Col: 8}, 12, 3)
	wantX0 := 5 + 1 + (8 - 2)
	if r2.X0 != wantX0 {
		t.Errorf("X0 = %d, want %d (scroll-adjusted column)", r2.X0, wantX0)
	}
}

// TestPopupRectAnchoredFlipsAboveAtBottom (boundary): when
// placing the dropdown below the cursor would push its bottom past the
// editor's bottom edge (vy1), it flips to render ABOVE the cursor line —
// ending at screen_y-1 (the cursor row) — and stays fully within view.
func TestPopupRectAnchoredFlipsAboveAtBottom(t *testing.T) {
	// Editor view (5,3)-(105,33): last usable row is 33. Cursor on Line
	// 28 (no scroll) -> below would be 33..41, exceeding 33. Flip above.
	r := anchoredRect(5, 3, 105, 33, 0, 0, editor.Position{Line: 28, Col: 8}, 12, 6)

	cursorScreenY := 3 + 1 + 28 // vy0 + 1 (frame) + (Line - oy)
	if r.Y1 > cursorScreenY {
		t.Errorf("Y1 = %d should end at or above cursor row %d when flipped", r.Y1, cursorScreenY)
	}
	if r.Y0 < 3 {
		t.Errorf("Y0 = %d escaped the top of the editor view (3)", r.Y0)
	}
	if r.Y1 > 33 {
		t.Errorf("Y1 = %d escaped the bottom of the editor view (33)", r.Y1)
	}
}

// TestPopupRectAnchoredLastVisibleRowFlips (boundary):
// cursor on the very last visible editor row must flip above (there is no
// room for even one row below).
func TestPopupRectAnchoredLastVisibleRowFlips(t *testing.T) {
	// Cursor screen row = vy0 + 1 + (Line-oy) = 3 + 1 + 29 = 33 = vy1
	// (the bottom frame row). One below = 34, exceeds vy1. Flip above.
	r := anchoredRect(5, 3, 105, 33, 0, 0, editor.Position{Line: 29, Col: 8}, 12, 4)
	if r.Y1 > 3+1+29 {
		t.Errorf("Y1 = %d should be above cursor row %d", r.Y1, 3+1+29)
	}
	if r.Y0 < 3 || r.Y1 > 33 {
		t.Errorf("rect (Y0=%d,Y1=%d) escaped editor bounds [3,33]", r.Y0, r.Y1)
	}
}

// TestPopupRectAnchoredClampsWithinView: the rect never
// exceeds the editor view rectangle on any edge, even for a wide popup
// near the right edge.
func TestPopupRectAnchoredClampsWithinView(t *testing.T) {
	// Cursor near the right edge with a wide content width.
	r := anchoredRect(5, 3, 105, 33, 0, 0, editor.Position{Line: 10, Col: 95}, 40, 3)
	if r.X0 < 5 || r.X1 > 105 {
		t.Errorf("rect X span (%d,%d) escaped editor bounds [5,105]", r.X0, r.X1)
	}
	if r.Y0 < 3 || r.Y1 > 33 {
		t.Errorf("rect Y span (%d,%d) escaped editor bounds [3,33]", r.Y0, r.Y1)
	}
}
