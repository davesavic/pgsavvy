package context

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/davesavic/dbsavvy/pkg/theme"
)

// ANSI SGR sequences for the side-rail search highlighter. These mirror the
// (unexported) grid constants in pkg/gui/grid/cells.go; they're replicated
// rather than imported because the grid versions are package-private. The
// rail* prefix avoids any collision inside the context package.
const (
	railAnsiReset     = "\x1b[0m"
	railAnsiDim       = "\x1b[2m"
	railAnsiBold      = "\x1b[1m"
	railAnsiItalic    = "\x1b[3m"
	railAnsiUnderline = "\x1b[4m"
)

// railSGRPrefixForStyle returns the SGR introducer for style, or "" when no
// escape is required. Mirrors grid/cells.go:sgrPrefixForStyle exactly: fg
// code, then bold, italic, underline, then bg code. Regression-credit:
// pkg/gui/grid/cells.go (sgrPrefixForStyle ~235).
func railSGRPrefixForStyle(s theme.Style) string {
	var sb strings.Builder
	if code := railAnsiFgCode(s.Fg); code != "" {
		sb.WriteString(code)
	}
	if s.Bold {
		sb.WriteString(railAnsiBold)
	}
	if s.Italic {
		sb.WriteString(railAnsiItalic)
	}
	if s.Underline {
		sb.WriteString(railAnsiUnderline)
	}
	if code := railAnsiBgCode(s.Bg); code != "" {
		sb.WriteString(code)
	}
	return sb.String()
}

// railAnsiFgCode maps a W3C colour name to its ANSI SGR foreground escape.
// Hex/unknown tokens collapse to "". Faithful copy of
// pkg/gui/grid/cells.go:ansiFgCode (~258).
func railAnsiFgCode(fg string) string {
	switch strings.ToLower(fg) {
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

// railAnsiBgCode maps a colour token to its ANSI background SGR escape. A
// `#RRGGBB` hex value becomes a truecolor sequence; W3C names fall back to
// the basic 40-47 / bright 100 codes. Faithful copy of
// pkg/gui/grid/cells.go:ansiBgCode (~287).
func railAnsiBgCode(bg string) string {
	if code := railAnsiTrueColorBg(bg); code != "" {
		return code
	}
	switch strings.ToLower(bg) {
	case "black":
		return "\x1b[40m"
	case "red":
		return "\x1b[41m"
	case "green":
		return "\x1b[42m"
	case "yellow":
		return "\x1b[43m"
	case "blue":
		return "\x1b[44m"
	case "magenta":
		return "\x1b[45m"
	case "cyan":
		return "\x1b[46m"
	case "white":
		return "\x1b[47m"
	case "brightblack":
		return "\x1b[100m"
	default:
		return ""
	}
}

// railAnsiTrueColorBg returns the 48;2;R;G;B background escape for a
// `#RRGGBB` token, or "" when bg isn't a well-formed 6-digit hex colour.
// Faithful copy of pkg/gui/grid/cells.go:ansiTrueColorBg (~317).
func railAnsiTrueColorBg(bg string) string {
	if len(bg) != 7 || bg[0] != '#' {
		return ""
	}
	rgb, err := hex.DecodeString(bg[1:])
	if err != nil {
		return ""
	}
	return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", rgb[0], rgb[1], rgb[2])
}

// railMatchStyle returns the highlight Style for a match span: CurSearch for
// the current match, SearchHighlight otherwise. Mirrors
// pkg/gui/grid/search_highlight.go:matchStyle (~189).
func railMatchStyle(current bool) theme.Style {
	t := theme.Current()
	if current {
		return dereferenceRailStyle(t.CurSearch)
	}
	return dereferenceRailStyle(t.SearchHighlight)
}

// dereferenceRailStyle returns the value pointed to by s, or the zero Style
// when s is nil.
func dereferenceRailStyle(s *theme.Style) theme.Style {
	if s == nil {
		return theme.Style{}
	}
	return *s
}

// railHighlightSpan is one search-match span to paint inside a rail name,
// expressed as BYTE offsets into the (sanitized) name string. current marks
// the span the cursor is on — it gets the stronger CurSearch style.
type railHighlightSpan struct {
	start   int
	end     int
	current bool
}

// renderRailName paints name with the search-highlight spans, composed
// with the disconnected dim. spans are byte offsets into name (rune-
// boundary-safe, ascending, non-overlapping — matcher guarantees).
//
// When spans is empty the output is byte-identical to the pre-feature
// render: name (connected) or "\x1b[2m"+name+"\x1b[0m" (disconnected).
//
// Disconnected composition: a dim
// baseline "\x1b[2m"; each highlight span CLOSES by restoring dim —
// emit "\x1b[0m" (full reset, clears the CurSearch black-on-yellow bg)
// then re-open "\x1b[2m" — so trailing bytes stay dim and no bg SGR
// leaks past the span. The row ends with a final "\x1b[0m".
func renderRailName(name string, dim bool, spans []railHighlightSpan) string {
	if len(spans) == 0 {
		if !dim {
			return name
		}
		return railAnsiDim + name + railAnsiReset
	}

	var b strings.Builder
	if dim {
		b.WriteString(railAnsiDim)
	}
	pos := 0
	for _, s := range spans {
		// Guard out-of-range spans defensively so a stale offset can't
		// panic — the matcher + SetItems-clears invariants make this rare.
		if s.start < 0 || s.end > len(name) || s.end < s.start || s.start < pos {
			continue
		}
		b.WriteString(name[pos:s.start])
		b.WriteString(railSGRPrefixForStyle(railMatchStyle(s.current)))
		b.WriteString(name[s.start:s.end])
		b.WriteString(railAnsiReset)
		if dim {
			b.WriteString(railAnsiDim)
		}
		pos = s.end
	}
	b.WriteString(name[pos:])
	if dim {
		b.WriteString(railAnsiReset)
	}
	return b.String()
}
