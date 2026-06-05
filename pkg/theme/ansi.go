package theme

import "strings"

// AnsiReset is the ANSI SGR sequence that clears all attributes. Pair it with
// the escape returned by AnsiFgSGR to bound a tinted span.
const AnsiReset = "\x1b[0m"

// AnsiFgSGR maps a standard 8-colour name onto its ANSI foreground SGR escape.
// Unknown tokens (hex codes, names outside the standard palette, empty) return
// "" so callers can fall back to no tinting. This is the single source of truth
// for name → SGR conversion across the gui (status bar, grid cells, layout,
// connection rows).
func AnsiFgSGR(name string) string {
	switch strings.ToLower(name) {
	case "black":
		return "\x1b[30m"
	case "red":
		return "\x1b[31m"
	case "green":
		return "\x1b[32m"
	case "yellow":
		return "\x1b[33m"
	case "blue":
		return "\x1b[34m"
	case "magenta":
		return "\x1b[35m"
	case "cyan":
		return "\x1b[36m"
	case "white":
		return "\x1b[37m"
	default:
		return ""
	}
}
