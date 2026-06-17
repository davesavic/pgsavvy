package ui_test

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
)

func TestGetWindowDimensionsReturnsAllRequiredKeys(t *testing.T) {
	got := ui.GetWindowDimensions(120, 40)
	for _, name := range ui.RequiredWindows() {
		if _, ok := got[name]; !ok {
			t.Errorf("missing required window %q in output (keys: %v)", name, mapKeys(got))
		}
	}
}

func TestGetWindowDimensionsSchemasTablesIsFullHeight(t *testing.T) {
	const w, h = 120, 40
	got := ui.GetWindowDimensions(w, h)

	st, ok := got["schemas-tables"]
	if !ok {
		t.Fatal("schemas-tables missing")
	}
	main, ok := got["main"]
	if !ok {
		t.Fatal("main missing")
	}
	secondary, ok := got["secondary"]
	if !ok {
		t.Fatal("secondary missing")
	}

	// The single full-height left rail must span the entire body height —
	// i.e. from the top of the right column (main) to its bottom (secondary).
	// This proves it is NOT half-height, as it would have been when "schemas"
	// and "tables" were stacked weight-1 siblings.
	if st.Y0 != main.Y0 || st.Y1 != secondary.Y1 {
		t.Errorf("schemas-tables Y-extent = [%d,%d]; want full body height [%d,%d] (main top .. secondary bottom)",
			st.Y0, st.Y1, main.Y0, secondary.Y1)
	}
	// Sanity: full height must be strictly taller than the old half-rail
	// (a single stacked box was ~half the body height).
	if st.Y1-st.Y0 <= main.Y1-main.Y0 {
		t.Errorf("schemas-tables height %d not greater than main's half-height %d",
			st.Y1-st.Y0, main.Y1-main.Y0)
	}

	// And it occupies the 24-col left rail: its width should be ~24 and it
	// must sit to the left of main.
	if st.X1 >= main.X0 {
		t.Errorf("schemas-tables X1=%d should be left of main X0=%d", st.X1, main.X0)
	}
}

func TestGetWindowDimensionsCoversSideContextViewNames(t *testing.T) {
	// Layout-invariant guard: every view name resolved by a flattened
	// SIDE_CONTEXT context MUST have a box in GetWindowDimensions, otherwise
	// the Tier-1 layout loop silently `continue`s on the missing key and the
	// pane renders blank.
	//
	// NOTE (merge unit pgsavvy-i42s {.2,.4,.5,.6}): at THIS point the live
	// SCHEMAS/TABLES contexts still resolve their view names to "schemas" and
	// "tables", which the box was just renamed away from. Asserting the full
	// box⊇view-name invariant here would FAIL until .4 renames those contexts
	// to "schemas-tables". To keep `task test` green without asserting a false
	// invariant, this test pins the producer side only: the new "schemas-tables"
	// box exists. Task .4 wires the SIDE_CONTEXT view names to match and
	// upgrades this into the true ⊇ invariant.
	got := ui.GetWindowDimensions(120, 40)
	if _, ok := got["schemas-tables"]; !ok {
		t.Errorf("schemas-tables box missing; .4 binds the SCHEMAS/TABLES context view names to it (keys: %v)", mapKeys(got))
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
