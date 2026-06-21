package theme

import "testing"

func TestLayerFgIsZeroValue(t *testing.T) {
	var l Layer
	if l != Fg {
		t.Fatalf("zero-value Layer = %d, want Fg (%d)", l, Fg)
	}
	if Fg != 0 {
		t.Fatalf("Fg = %d, want 0", Fg)
	}
}

func TestClassifyColor(t *testing.T) {
	tests := []struct {
		token       string
		wantKind    Kind
		wantPayload ColorPayload
	}{
		// Named16 palette index 0..15.
		{"black", Named16, ColorPayload{Palette: 0}},
		{"white", Named16, ColorPayload{Palette: 7}},
		{"brightblack", Named16, ColorPayload{Palette: 8}},
		{"brightwhite", Named16, ColorPayload{Palette: 15}},
		{"brightred", Named16, ColorPayload{Palette: 9}},
		{"gray", Named16, ColorPayload{Palette: 8}},
		{"grey", Named16, ColorPayload{Palette: 8}},
		// case-insensitive
		{"RED", Named16, ColorPayload{Palette: 1}},
		{"BrightRed", Named16, ColorPayload{Palette: 9}},
		// Index256
		{"color0", Index256, ColorPayload{Index: 0}},
		{"color42", Index256, ColorPayload{Index: 42}},
		{"color255", Index256, ColorPayload{Index: 255}},
		{"color007", Index256, ColorPayload{Index: 7}},
		{"COLOR42", Index256, ColorPayload{Index: 42}},
		// Hex
		{"#abc", Hex, ColorPayload{R: 170, G: 187, B: 204}},
		{"#ABC", Hex, ColorPayload{R: 170, G: 187, B: 204}},
		{"#ff8800", Hex, ColorPayload{R: 255, G: 136, B: 0}},
		// Empty
		{"", Empty, ColorPayload{}},
		// Unknown — out of range / malformed colorN
		{"color256", Unknown, ColorPayload{}},
		{"color-1", Unknown, ColorPayload{}},
		{"color+7", Unknown, ColorPayload{}},
		{"color ", Unknown, ColorPayload{}},
		{"color1.0", Unknown, ColorPayload{}},
		{"colorx", Unknown, ColorPayload{}},
		{"color", Unknown, ColorPayload{}},
		// Unknown — malformed hex
		{"#ab", Unknown, ColorPayload{}},
		{"#abcd", Unknown, ColorPayload{}},
		{"#gggggg", Unknown, ColorPayload{}},
		// Unknown — generic
		{"notacolor", Unknown, ColorPayload{}},
		{"ff8800", Unknown, ColorPayload{}},
		// Unknown — injection
		{"31m\x1b]0;x\x07", Unknown, ColorPayload{}},
		{"1;31", Unknown, ColorPayload{}},
	}
	for _, tt := range tests {
		t.Run(tt.token, func(t *testing.T) {
			gotKind, gotPayload := ClassifyColor(tt.token)
			if gotKind != tt.wantKind {
				t.Fatalf("ClassifyColor(%q) kind = %d, want %d", tt.token, gotKind, tt.wantKind)
			}
			if gotPayload != tt.wantPayload {
				t.Fatalf("ClassifyColor(%q) payload = %+v, want %+v", tt.token, gotPayload, tt.wantPayload)
			}
		})
	}
}

func TestColorSGR(t *testing.T) {
	tests := []struct {
		token string
		layer Layer
		want  string
	}{
		// named basic
		{"red", Fg, "\x1b[31m"},
		{"red", Bg, "\x1b[41m"},
		{"yellow", Fg, "\x1b[33m"},
		{"yellow", Bg, "\x1b[43m"},
		// named bright
		{"brightblack", Fg, "\x1b[90m"},
		{"brightwhite", Bg, "\x1b[107m"},
		{"brightred", Fg, "\x1b[91m"},
		{"brightcyan", Bg, "\x1b[106m"},
		// gray/grey alias
		{"gray", Fg, "\x1b[90m"},
		{"grey", Fg, "\x1b[90m"},
		{"gray", Bg, "\x1b[100m"},
		{"grey", Bg, "\x1b[100m"},
		// 256
		{"color42", Fg, "\x1b[38;5;42m"},
		{"color42", Bg, "\x1b[48;5;42m"},
		{"color0", Fg, "\x1b[38;5;0m"},
		{"color255", Fg, "\x1b[38;5;255m"},
		// hex
		{"#abc", Fg, "\x1b[38;2;170;187;204m"},
		{"#abc", Bg, "\x1b[48;2;170;187;204m"},
		{"#ff8800", Bg, "\x1b[48;2;255;136;0m"},
		// case-insensitive
		{"RED", Fg, "\x1b[31m"},
		{"BrightRed", Fg, "\x1b[91m"},
		{"#ABC", Fg, "\x1b[38;2;170;187;204m"},
		{"COLOR42", Fg, "\x1b[38;5;42m"},
		// empty + unknown
		{"", Fg, ""},
		{"notacolor", Fg, ""},
		{"#gggggg", Fg, ""},
		{"ff8800", Fg, ""},
		{"color256", Fg, ""},
		{"color-1", Fg, ""},
		{"color+7", Fg, ""},
		{"#ab", Fg, ""},
		{"#abcd", Fg, ""},
		// injection
		{"31m\x1b]0;x\x07", Fg, ""},
		{"1;31", Fg, ""},
	}
	for _, tt := range tests {
		t.Run(tt.token+"/"+layerName(tt.layer), func(t *testing.T) {
			got := ColorSGR(tt.token, tt.layer)
			if got != tt.want {
				t.Fatalf("ColorSGR(%q, %v) = %q, want %q", tt.token, tt.layer, got, tt.want)
			}
		})
	}
}

func TestColorParamSGR(t *testing.T) {
	tests := []struct {
		token string
		layer Layer
		want  string
	}{
		{"yellow", Fg, "33"},
		{"yellow", Bg, "43"},
		{"red", Fg, "31"},
		{"brightblack", Fg, "90"},
		{"color42", Fg, "38;5;42"},
		{"color42", Bg, "48;5;42"},
		{"#ff8800", Fg, "38;2;255;136;0"},
		{"#ff8800", Bg, "48;2;255;136;0"},
		// empty + unknown
		{"", Fg, ""},
		{"notacolor", Fg, ""},
		{"#gggggg", Fg, ""},
		{"ff8800", Fg, ""},
		// injection
		{"31m\x1b]0;x\x07", Fg, ""},
		{"1;31", Fg, ""},
	}
	for _, tt := range tests {
		t.Run(tt.token+"/"+layerName(tt.layer), func(t *testing.T) {
			got := ColorParamSGR(tt.token, tt.layer)
			if got != tt.want {
				t.Fatalf("ColorParamSGR(%q, %v) = %q, want %q", tt.token, tt.layer, got, tt.want)
			}
			// Bare param must never contain escape introducer or terminator.
			if got != "" {
				if contains(got, "\x1b[") {
					t.Fatalf("ColorParamSGR(%q) = %q contains \\x1b[", tt.token, got)
				}
				if contains(got, "m") {
					t.Fatalf("ColorParamSGR(%q) = %q contains 'm'", tt.token, got)
				}
			}
		})
	}
}

func layerName(l Layer) string {
	if l == Bg {
		return "bg"
	}
	return "fg"
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
