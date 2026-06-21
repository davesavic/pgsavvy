package theme

import (
	"strconv"
	"strings"
)

// Layer selects whether a color token resolves to a foreground or background
// SGR parameter. Fg is the zero value so a zero-arg / default resolves as
// foreground.
type Layer int

const (
	// Fg is the foreground layer (zero value).
	Fg Layer = iota
	// Bg is the background layer.
	Bg
)

// Kind classifies a color token into one of the supported vocabularies.
type Kind int

const (
	// Empty is the empty token "" (no color).
	Empty Kind = iota
	// Named16 is one of the 16 named ANSI colors (8 basic + 8 bright).
	Named16
	// Index256 is a "colorN" 256-palette index (N in 0..255).
	Index256
	// Hex is a "#rgb" or "#rrggbb" 24-bit truecolor token.
	Hex
	// Unknown is any token that is not a valid color (also injection-unsafe
	// tokens such as those containing ';', 'm', or escape bytes).
	Unknown
)

// ColorPayload is the shared cross-emitter classification contract returned by
// ClassifyColor. It is consumed by BOTH the SGR emitter in this package and the
// border emitter in a different package (the truecolor / 256 / named border
// path), so every field is a plain int usable cross-package.
//
// Only the field(s) relevant to the returned Kind are meaningful:
//   - Named16:  Palette holds the palette index 0..15. The 8 basic colors map
//     black=0..white=7; the 8 bright colors map brightblack=8..brightwhite=15;
//     gray/grey alias to 8 (brightblack). This palette index is the canonical
//     value the border path uses; the inline SGR path emits 30-37 / 90-97
//     (fg) and 40-47 / 100-107 (bg) instead.
//   - Index256: Index holds the validated index 0..255.
//   - Hex:      R, G, B hold the 8-bit channel values 0..255 (#rgb expanded to
//     #rrggbb for both layers).
//
// For Empty and Unknown all fields are zero.
//
// Note on bold: bold is emitted by callers as-authored. ClassifyColor and the
// SGR emitters never suppress or imply bold based on a bright color name.
type ColorPayload struct {
	// Palette is the 0..15 named-color index (Named16 only).
	Palette int
	// Index is the 0..255 palette index (Index256 only).
	Index int
	// R, G, B are the 0..255 channel values (Hex only).
	R, G, B int
}

// ClassifyColor classifies a color token into a Kind plus its ColorPayload.
//
// The token is lowercased internally because theme.Style stores Fg/Bg verbatim
// (only switch keys were lowercased upstream). Classification is allocation-free
// on the hot path: it runs per grid cell and per syntax token, so it uses only a
// switch plus strconv parsing — no regexp compilation and no map allocation.
//
// The returned ColorPayload is the shared cross-emitter contract; see its
// godoc. Injection-unsafe tokens (containing ';', 'm', escape/OSC bytes, etc.)
// fall through every strict branch and classify as Unknown, so no raw token can
// ever reach an escape sequence.
func ClassifyColor(token string) (Kind, ColorPayload) {
	if token == "" {
		return Empty, ColorPayload{}
	}

	t := strings.ToLower(token)

	if p, ok := named16Index(t); ok {
		return Named16, ColorPayload{Palette: p}
	}

	if rest, ok := strings.CutPrefix(t, "color"); ok {
		return classifyIndex256(rest)
	}

	if strings.HasPrefix(t, "#") {
		return classifyHex(t)
	}

	return Unknown, ColorPayload{}
}

// named16Index maps a lowercased named color to its 0..15 palette index.
func named16Index(t string) (int, bool) {
	switch t {
	case "black":
		return 0, true
	case "red":
		return 1, true
	case "green":
		return 2, true
	case "yellow":
		return 3, true
	case "blue":
		return 4, true
	case "magenta":
		return 5, true
	case "cyan":
		return 6, true
	case "white":
		return 7, true
	case "brightblack", "gray", "grey":
		return 8, true
	case "brightred":
		return 9, true
	case "brightgreen":
		return 10, true
	case "brightyellow":
		return 11, true
	case "brightblue":
		return 12, true
	case "brightmagenta":
		return 13, true
	case "brightcyan":
		return 14, true
	case "brightwhite":
		return 15, true
	default:
		return 0, false
	}
}

// classifyIndex256 validates the digits following a stripped "color" prefix.
// The remainder must be all digits and the parsed value in 0..255.
func classifyIndex256(rest string) (Kind, ColorPayload) {
	if rest == "" || !allDigits(rest) {
		return Unknown, ColorPayload{}
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 0 || n > 255 {
		return Unknown, ColorPayload{}
	}
	return Index256, ColorPayload{Index: n}
}

// classifyHex validates a "#rgb" or "#rrggbb" token (digits already lowercased).
func classifyHex(t string) (Kind, ColorPayload) {
	s := t[1:]
	if len(s) == 3 {
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	}
	if len(s) != 6 {
		return Unknown, ColorPayload{}
	}
	r, err1 := strconv.ParseUint(s[0:2], 16, 8)
	g, err2 := strconv.ParseUint(s[2:4], 16, 8)
	b, err3 := strconv.ParseUint(s[4:6], 16, 8)
	if err1 != nil || err2 != nil || err3 != nil {
		return Unknown, ColorPayload{}
	}
	return Hex, ColorPayload{R: int(r), G: int(g), B: int(b)}
}

// allDigits reports whether s is non-empty and every byte is an ASCII digit.
func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ColorParamSGR returns the BARE SGR parameter for a color token on the given
// layer — e.g. "33", "90", "38;5;42", "38;2;255;136;0" — or "" for empty and
// unknown tokens. The result never contains "\x1b[" or a trailing "m"; callers
// compose it into a larger escape (the highlight path appends it after
// bold/italic/underline params). Bold is the caller's concern; this never emits
// it.
func ColorParamSGR(token string, layer Layer) string {
	kind, p := ClassifyColor(token)
	switch kind {
	case Named16:
		return named16Param(p.Palette, layer)
	case Index256:
		return index256Param(p.Index, layer)
	case Hex:
		return hexParam(p.R, p.G, p.B, layer)
	default:
		return ""
	}
}

// ColorSGR returns the full "\x1b[<param>m" escape for a color token on the
// given layer, or "" when ColorParamSGR returns "".
func ColorSGR(token string, layer Layer) string {
	param := ColorParamSGR(token, layer)
	if param == "" {
		return ""
	}
	return "\x1b[" + param + "m"
}

// named16Param emits the SGR param for a 0..15 palette index. Basic colors
// (0..7) emit 30-37 (fg) / 40-47 (bg); bright colors (8..15) emit 90-97 (fg) /
// 100-107 (bg).
func named16Param(palette int, layer Layer) string {
	if palette < 8 {
		base := 30
		if layer == Bg {
			base = 40
		}
		return strconv.Itoa(base + palette)
	}
	base := 90
	if layer == Bg {
		base = 100
	}
	return strconv.Itoa(base + palette - 8)
}

// index256Param emits "38;5;N" (fg) or "48;5;N" (bg).
func index256Param(index int, layer Layer) string {
	prefix := "38;5;"
	if layer == Bg {
		prefix = "48;5;"
	}
	return prefix + strconv.Itoa(index)
}

// hexParam emits "38;2;r;g;b" (fg) or "48;2;r;g;b" (bg).
func hexParam(r, g, b int, layer Layer) string {
	prefix := "38;2;"
	if layer == Bg {
		prefix = "48;2;"
	}
	return prefix + strconv.Itoa(r) + ";" + strconv.Itoa(g) + ";" + strconv.Itoa(b)
}
