package grid

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/theme"
)

// ANSI SGR sequences shared with pkg/gui/orchestrator/status_render.go.
// We don't import that package — the constants are short and the
// alternative would create an import cycle (orchestrator already
// imports grid via the gui composition root).
const (
	ansiReset      = "\x1b[0m"
	ansiItalic     = "\x1b[3m"
	ansiBold       = "\x1b[1m"
	ansiUnderline  = "\x1b[4m"
	ansiReverseVid = "\x1b[7m"
)

// cellKind classifies a column for type-aware styling. Resolved from
// ColumnMeta.TypeName so we stay driver-agnostic — pg, sqlite, mysql
// can all share the same mapping by registering their TypeName strings.
type cellKind int

const (
	kindUnknown cellKind = iota
	kindNumeric
	kindString
	kindKeyword // boolean / enum literals
	kindJSON
	kindBytes
)

// classifyColumn maps a ColumnMeta to its display kind. Lowercased
// TypeName lookup; unrecognised types collapse to kindUnknown which
// renders with the default foreground.
func classifyColumn(col models.ColumnMeta) cellKind {
	switch strings.ToLower(col.TypeName) {
	case "int2", "int4", "int8", "int", "integer", "bigint", "smallint",
		"float4", "float8", "real", "double precision", "numeric", "decimal":
		return kindNumeric
	case "text", "varchar", "char", "bpchar", "name", "citext", "uuid":
		return kindString
	case "bool", "boolean":
		return kindKeyword
	case "json", "jsonb":
		return kindJSON
	case "bytea", "blob", "bytes":
		return kindBytes
	default:
		return kindUnknown
	}
}

// renderCell returns the styled string for a single cell. The string
// contains inline ANSI SGR escapes; gocui's escape interpreter lifts
// these into per-cell Attribute values. The returned string is *not*
// padded — the caller pads to column width after the escapes are
// stripped from the width calculation.
//
// Returns (visible, ansi-decorated). The visible string is the same
// content without SGR escapes, used for width computation.
func renderCell(value any, col models.ColumnMeta) (visible, decorated string) {
	visible = renderCellPlain(value, col)
	style := styleForCell(value, col)
	decorated = wrapWithStyle(visible, style)
	return visible, decorated
}

// renderCellPlain is the unstyled cell stringifier. Used for column
// auto-sizing (where SGR escapes would skew the width) and for TSV
// yank output (which must not carry colour codes).
func renderCellPlain(value any, col models.ColumnMeta) string {
	if value == nil {
		return "NULL"
	}
	switch classifyColumn(col) {
	case kindBytes:
		return renderBytesCell(value)
	case kindJSON:
		s := fmt.Sprintf("%v", value)
		if len(s) > MaxCellRenderBytes {
			s = s[:MaxCellRenderBytes-1] + "…"
		}
		return s
	default:
		s := fmt.Sprintf("%v", value)
		if len(s) > MaxCellRenderBytes {
			s = s[:MaxCellRenderBytes-1] + "…"
		}
		return s
	}
}

// renderBytesCell produces the `\x48656c6c6f… (5B)` preview spec'd in
// the cells doc-comment. Handles []byte directly; other types fall
// back through fmt.Sprintf, which still gives a usable preview.
func renderBytesCell(value any) string {
	if b, ok := value.([]byte); ok {
		size := len(b)
		const previewBytes = 8
		head := b
		truncated := false
		if len(head) > previewBytes {
			head = head[:previewBytes]
			truncated = true
		}
		hexed := hex.EncodeToString(head)
		if truncated {
			hexed += "…"
		}
		return fmt.Sprintf(`\x%s (%dB)`, hexed, size)
	}
	return fmt.Sprintf("%v", value)
}

// styleForCell returns the *theme.Style applied to the cell's content.
// NULL values always get the NullValue style with italic flagged on;
// non-NULL cells get the type-aware style from the active theme.
// Returns a *Style value (never nil) so callers can wrap with
// wrapWithStyle unconditionally.
func styleForCell(value any, col models.ColumnMeta) theme.Style {
	t := theme.Current()
	if value == nil {
		base := dereferenceStyle(t.NullValueFg)
		base.Italic = true
		return base
	}
	switch classifyColumn(col) {
	case kindNumeric:
		return dereferenceStyle(t.NumericFg)
	case kindKeyword:
		return dereferenceStyle(t.KeywordFg)
	case kindString, kindJSON:
		return dereferenceStyle(t.StringFg)
	default:
		return theme.Style{}
	}
}

// dereferenceStyle returns the value pointed to by s, or the zero
// Style when s is nil. Centralises the nil-pointer dance so callers
// can deal in values.
func dereferenceStyle(s *theme.Style) theme.Style {
	if s == nil {
		return theme.Style{}
	}
	return *s
}

// wrapWithStyle wraps s in ANSI SGR escapes matching style. When the
// style is the zero value the input is returned unchanged so cells
// without colour don't carry redundant reset sequences. The escape
// vocabulary mirrors pkg/gui/orchestrator/status_render.ansiSGRForStyle
// — only the foreground colour is honored from named W3C colours plus
// the italic flag (used for NULL).
func wrapWithStyle(s string, style theme.Style) string {
	prefix := sgrPrefixForStyle(style)
	if prefix == "" {
		return s
	}
	return prefix + s + ansiReset
}

// sgrPrefixForStyle returns the SGR introducer for style, or "" when
// no escape is required (zero style or unrecognised colour). Italic
// is honored independently of foreground colour.
func sgrPrefixForStyle(s theme.Style) string {
	var sb strings.Builder
	if code := ansiFgCode(s.Fg); code != "" {
		sb.WriteString(code)
	}
	if s.Bold {
		sb.WriteString(ansiBold)
	}
	if s.Italic {
		sb.WriteString(ansiItalic)
	}
	if s.Underline {
		sb.WriteString(ansiUnderline)
	}
	return sb.String()
}

// ansiFgCode maps a W3C colour name to its ANSI SGR escape. Hex
// values and unknown tokens collapse to "" — same fallback policy as
// promptStyledLine in the orchestrator.
func ansiFgCode(fg string) string {
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

// applySelectionHighlight wraps cell in the SelectedRowBg style. The
// background is rendered via ANSI reverse-video because gocui's escape
// interpreter doesn't honor 48;5;N sequences for cell-background in
// all terminals; reverse-video swaps fg+bg and is universally
// supported. This is the same trick lazygit uses for its selection
// highlight when the theme background isn't set.
func applySelectionHighlight(decorated string) string {
	return ansiReverseVid + decorated + ansiReset
}
