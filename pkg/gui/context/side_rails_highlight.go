package context

import (
	"strings"

	"github.com/davesavic/pgsavvy/pkg/theme"
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
// param, then bold, italic, underline, then bg param. Foreground and
// background colour resolution is delegated to the unified theme resolver
// (ColorParamSGR); under NO_COLOR (theme.IsMonochrome) the colour params are
// suppressed while bold/italic/underline are kept.
func railSGRPrefixForStyle(s theme.Style) string {
	mono := theme.IsMonochrome()
	var sb strings.Builder
	if !mono {
		if param := theme.ColorParamSGR(s.Fg, theme.Fg); param != "" {
			sb.WriteString("\x1b[" + param + "m")
		}
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
	if !mono {
		if param := theme.ColorParamSGR(s.Bg, theme.Bg); param != "" {
			sb.WriteString("\x1b[" + param + "m")
		}
	}
	return sb.String()
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
