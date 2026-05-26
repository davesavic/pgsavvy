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

// renderCellWithDirty wraps renderCell with the dirty-cell decorator.
// When isDirty is true the decorated string is layered with the
// DirtyCellBg style and gains a trailing `●` glyph; visible carries the
// glyph as well so width budgeting downstream keeps decoration in sync
// with layout. When isDirty is false the result is identical to
// renderCell. dbsavvy-bwq.6 (A3).
func renderCellWithDirty(value any, col models.ColumnMeta, isDirty bool) (visible, decorated string) {
	visible, decorated = renderCell(value, col)
	if !isDirty {
		return visible, decorated
	}
	dirtyStyle := dereferenceStyle(theme.Current().DirtyCellBg)
	visible = visible + dirtyCellMarker
	decorated = DecorateDirtyCell(decorated, true, dirtyStyle)
	return visible, decorated
}

// renderCellPadded renders value for col, padded to display width w, with
// the type-aware cell style. When isDirty is true the dirty marker (`●`) is
// appended inside the width budget and the DirtyCellBg tint is layered over
// the cell so a staged (unsaved) edit reads as dirty. Padding the plain
// visible string before wrapping mirrors renderDataLine's clean path so a
// digit in the SGR prefix can never collide with a padded value.
// dbsavvy-cyh (A3 wiring).
func renderCellPadded(value any, col models.ColumnMeta, w int, isDirty bool) string {
	visible := renderCellPlain(value, col)
	if isDirty {
		visible += dirtyCellMarker
	}
	padded := padRight(visible, w)
	styled := wrapWithStyle(padded, styleForCell(value, col))
	if isDirty {
		styled = wrapWithStyle(styled, dereferenceStyle(theme.Current().DirtyCellBg))
	}
	return styled
}

// renderCellPlain is the unstyled cell stringifier. Used for column
// auto-sizing (where SGR escapes would skew the width) and for TSV
// yank output (which must not carry colour codes). All non-NULL
// strings are routed through SanitizeCellEscapes (dbsavvy-uv0 AD-16)
// so untrusted server output cannot bleed terminal escapes into the
// grid or exports.
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
		return SanitizeCellEscapes(s)
	default:
		s, ok := FormatArrayLiteral(value)
		if !ok {
			s = fmt.Sprintf("%v", value)
		}
		if len(s) > MaxCellRenderBytes {
			s = s[:MaxCellRenderBytes-1] + "…"
		}
		return SanitizeCellEscapes(s)
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

// SanitizeCellEscapes strips ANSI escape introducers and C0 control
// characters from s so untrusted server output cannot hijack the
// terminal. Used by renderExpanded, grid cell passthrough, yank, EXPLAIN
// raw text, and exporters (dbsavvy-uv0 AD-16).
//
// Rules:
//   - CSI sequences (\x1b[ ... final-byte) are dropped wholesale.
//   - OSC sequences (\x1b] ... BEL or ESC \) are dropped wholesale.
//   - Other \x1b-prefixed escapes are dropped along with their single
//     following byte (covers \x1b(B, \x1b)A, ESC-only, etc.).
//   - C0 control characters (0x00-0x1F and 0x7F) are removed, EXCEPT
//     \t (0x09) and \n (0x0A) which carry legitimate meaning in cell
//     content (TSV yank, multi-line JSON).
func SanitizeCellEscapes(s string) string {
	if s == "" {
		return s
	}
	if !needsSanitize(s) {
		return s
	}
	var sb strings.Builder
	sb.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c == 0x1b {
			// ESC; skip the sequence.
			i = skipEscapeSequence(s, i)
			continue
		}
		if c == '\t' || c == '\n' {
			sb.WriteByte(c)
			i++
			continue
		}
		if c < 0x20 || c == 0x7f {
			// Drop other C0 controls (incl. \r, BEL, etc).
			i++
			continue
		}
		sb.WriteByte(c)
		i++
	}
	return sb.String()
}

// needsSanitize is a fast path: scan for any byte that would trigger
// the stripper. Identity-preserving inputs return false and skip the
// allocation in SanitizeCellEscapes.
func needsSanitize(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == 0x1b || c == 0x7f {
			return true
		}
		if c < 0x20 && c != '\t' && c != '\n' {
			return true
		}
	}
	return false
}

// skipEscapeSequence consumes the escape sequence starting at s[i]
// (where s[i] == 0x1b) and returns the index past the last consumed
// byte. Handles CSI, OSC, and lone ESC + one byte.
func skipEscapeSequence(s string, i int) int {
	// Already at ESC.
	j := i + 1
	if j >= len(s) {
		return j
	}
	switch s[j] {
	case '[':
		// CSI: consume params and intermediates until the final byte
		// in 0x40..0x7E.
		j++
		for j < len(s) {
			c := s[j]
			j++
			if c >= 0x40 && c <= 0x7e {
				return j
			}
		}
		return j
	case ']':
		// OSC: terminated by BEL (0x07) or ESC \ (0x1b 0x5c).
		j++
		for j < len(s) {
			c := s[j]
			if c == 0x07 {
				return j + 1
			}
			if c == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2
			}
			j++
		}
		return j
	case '(', ')', '*', '+':
		// SCS — Select Character Set: ESC <designator> <final>. Drop all
		// three bytes (the final byte carries the charset identifier).
		if j+1 < len(s) {
			return j + 2
		}
		return j + 1
	default:
		// Two-byte escape (lone ESC + one byte) — drop both.
		return j + 1
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
