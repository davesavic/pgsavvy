package theme

import "testing"

// TestColorSGR_Hex covers hex foreground tokens resolving to a 24-bit truecolor
// escape; non-hex tokens (missing '#', wrong length, non-hex digits) still
// resolve to "". "red" is now a valid named token under the unified resolver,
// so it is asserted separately (it tints, unlike a hex-only check which
// returned "").
func TestColorSGR_Hex(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"#ff4d4d", "\x1b[38;2;255;77;77m"},
		{"#abc", "\x1b[38;2;170;187;204m"},
		{"#000000", "\x1b[38;2;0;0;0m"},
		{"#FFFFFF", "\x1b[38;2;255;255;255m"},
		{"ff4d4d", ""},  // missing '#'
		{"#ff4d", ""},   // wrong length
		{"#gggggg", ""}, // non-hex digits
		{"", ""},        // empty
	}
	for _, c := range cases {
		if got := ColorSGR(c.in, Fg); got != c.want {
			t.Errorf("ColorSGR(%q, Fg) = %q, want %q", c.in, got, c.want)
		}
	}
	// "red" was "" under the old hex-only helper; under the unified resolver it
	// is a named token and tints to the basic-palette foreground escape.
	if got, want := ColorSGR("red", Fg), "\x1b[31m"; got != want {
		t.Errorf("ColorSGR(%q, Fg) = %q, want %q", "red", got, want)
	}
}
