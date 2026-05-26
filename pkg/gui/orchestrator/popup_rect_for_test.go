package orchestrator

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// TestPopupRectFor_TableInspect locks in the 60% × 60% rect for the
// TABLE_INSPECT popup (epic dbsavvy-3vf, task .1). Uses a 100×100
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
// for the CELL_EDITOR popup (dbsavvy-tzi.1). The core property is the
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
// COMMIT_DIALOG popup (dbsavvy-b0l). Without a popupRectFor case the
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
