package ui_test

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
)

func TestGetWindowDimensionsReturnsAllRequiredKeys(t *testing.T) {
	got := ui.GetWindowDimensions(120, 40)
	for _, name := range ui.RequiredWindows() {
		if _, ok := got[name]; !ok {
			t.Errorf("missing required window %q in output (keys: %v)", name, mapKeys(got))
		}
	}
}

func TestGetWindowDimensionsPopupOverlayCoversScreen(t *testing.T) {
	const w, h = 120, 40
	got := ui.GetWindowDimensions(w, h)
	d, ok := got["popup-overlay"]
	if !ok {
		t.Fatal("popup-overlay missing")
	}
	if d.X0 != 0 || d.Y0 != 0 || d.X1 != w-1 || d.Y1 != h-1 {
		t.Errorf("popup-overlay = %+v; want full-screen (0,0,%d,%d)", d, w-1, h-1)
	}
}

func TestGetWindowDimensionsHandlesTinyScreen(t *testing.T) {
	got := ui.GetWindowDimensions(5, 5)
	// Even at 5x5 every required key must exist (zero-area is fine).
	for _, name := range ui.RequiredWindows() {
		if _, ok := got[name]; !ok {
			t.Errorf("missing required window %q on tiny screen", name)
		}
	}
}

func mapKeys(m map[string]ui.Dimensions) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
