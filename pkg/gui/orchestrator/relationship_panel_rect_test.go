package orchestrator

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// TestPopupRectFor_RelationshipPanelDocked verifies the docked rect anchors
// to the rightmost ~40% of the secondary (result-grid) canvas, full height.
func TestPopupRectFor_RelationshipPanelDocked(t *testing.T) {
	secondary := ui.Dimensions{X0: 10, Y0: 5, X1: 110, Y1: 55} // 100 wide, 50 tall
	dims := map[string]ui.Dimensions{"secondary": secondary}

	r, ok := popupRectFor(types.RELATIONSHIP_PANEL, dims, 200, 100)
	if !ok {
		t.Fatalf("popupRectFor(RELATIONSHIP_PANEL) ok=false, want true")
	}
	// Right-anchored: the panel's right edge coincides with the secondary
	// canvas's right edge.
	if r.X1 != secondary.X1 {
		t.Errorf("X1 = %d, want %d (right-anchored)", r.X1, secondary.X1)
	}
	// ~40% of the 100-wide canvas => ~40 cols.
	if w := r.X1 - r.X0; w < 38 || w > 42 {
		t.Errorf("width %d not ~40 (rightmost 40%%)", w)
	}
	// Full height of the secondary canvas.
	if r.Y0 != secondary.Y0 || r.Y1 != secondary.Y1 {
		t.Errorf("vertical span = [%d,%d], want full [%d,%d]", r.Y0, r.Y1, secondary.Y0, secondary.Y1)
	}
}

// TestPopupRectFor_RelationshipPanelFallback verifies that, when the
// secondary canvas is absent, the docked rect falls back to the rightmost
// slice of the full terminal canvas rather than reporting ok=false (so the
// wiring invariant + snapshot still see a non-zero rect).
func TestPopupRectFor_RelationshipPanelFallback(t *testing.T) {
	r, ok := popupRectFor(types.RELATIONSHIP_PANEL, map[string]ui.Dimensions{}, 100, 100)
	if !ok {
		t.Fatalf("popupRectFor(RELATIONSHIP_PANEL) fallback ok=false, want true")
	}
	if r == (rect{}) {
		t.Fatalf("fallback returned zero rect")
	}
	if r.X1 != 99 {
		t.Errorf("fallback X1 = %d, want 99 (full-canvas right edge)", r.X1)
	}
}
