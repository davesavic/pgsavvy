package theme

import "testing"

func TestAnsiFgHexSGR(t *testing.T) {
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
		{"red", ""},     // named, not hex
		{"", ""},        // empty
	}
	for _, c := range cases {
		if got := AnsiFgHexSGR(c.in); got != c.want {
			t.Errorf("AnsiFgHexSGR(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
