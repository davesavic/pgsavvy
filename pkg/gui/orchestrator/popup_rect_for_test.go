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
