package grid

import (
	"database/sql/driver"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/theme"
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

// Built-in PostgreSQL OIDs used as a fallback when TypeName is empty.
// Custom types / domains built on jsonb still carry the base OID even
// if pgtype's default Map doesn't have a name entry for them.
const (
	pgOIDJSON  = 114  // json
	pgOIDJSONB = 3802 // jsonb
	pgOIDBytea = 17   // bytea
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

// IsJSONColumn reports whether col is a json/jsonb column, using the same
// type classification as the grid renderer. The cell editor uses this to
// seed json/jsonb cells with JSON text via FormatJSONValue, since pgx may
// decode such a value into a []byte that the Go-shape fallbacks miss.
func IsJSONColumn(col models.ColumnMeta) bool {
	return classifyColumn(col) == kindJSON
}

// RenderCellText returns the unstyled, type-aware text representation of a
// cell value. Unlike fmt.Sprint, this uses column metadata to format jsonb,
// bytea, timestamps, arrays, etc. in a human-readable form. Exporters use
// this so exported output matches what the user sees in the grid.
func RenderCellText(value any, col models.ColumnMeta) string {
	return renderCellPlain(value, col)
}

// classifyColumn maps a ColumnMeta to its display kind. Lowercased
// TypeName lookup; unrecognised types collapse to kindUnknown which
// renders with the default foreground.
//
// When TypeName is empty (pgx couldn't resolve an OID — common with
// custom types / domains), a built-in OID fallback kicks in so jsonb,
// json, and bytea columns still render with their proper formatter
// instead of Go's %v byte-array dump.
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
		// OID-based fallback when TypeName is empty / unknown
		// (pgx can decode the OID but the default pgtype.Map may
		// not have a Name for domain / custom types built on
		// jsonb).
		switch col.TypeOID {
		case pgOIDJSON, pgOIDJSONB:
			return kindJSON
		case pgOIDBytea:
			return kindBytes
		}
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
// DirtyCellBg tint; the visible string is unchanged because the edit is
// signalled by background colour, not a glyph. When isDirty is false the
// result is identical to renderCell.
func renderCellWithDirty(value any, col models.ColumnMeta, isDirty bool) (visible, decorated string) {
	visible, decorated = renderCell(value, col)
	if !isDirty {
		return visible, decorated
	}
	dirtyStyle := dereferenceStyle(theme.Current().DirtyCellBg)
	decorated = DecorateDirtyCell(decorated, true, dirtyStyle)
	return visible, decorated
}

// renderCellPadded renders value for col, padded to display width w, with
// the type-aware cell style. When isDirty is true the DirtyCellBg tint is
// layered over the whole padded cell so a staged (unsaved) edit reads as
// dirty — the background covers the full column width, including any
// truncation ellipsis, so no per-cell glyph is needed. Padding the plain
// visible string before wrapping mirrors renderDataLine's clean path so a
// digit in the SGR prefix can never collide with a padded value.
func renderCellPadded(value any, col models.ColumnMeta, w int, isDirty bool) string {
	visible := renderCellPlain(value, col)
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
// strings are routed through SanitizeCellEscapes
// so untrusted server output cannot bleed terminal escapes into the
// grid or exports.
func renderCellPlain(value any, col models.ColumnMeta) string {
	if value == nil {
		return "NULL"
	}
	if t, ok := value.(time.Time); ok {
		return t.Format("2006-01-02 15:04:05.999999-07:00")
	}
	switch classifyColumn(col) {
	case kindBytes:
		return renderBytesCell(value)
	case kindJSON:
		s := FormatJSONValue(value)
		s = capCellBytes(s)
		return SanitizeCellEscapes(s)
	default:
		s, ok := FormatArrayLiteral(value)
		if !ok {
			s = formatScalar(value)
		}
		s = capCellBytes(s)
		return SanitizeCellEscapes(s)
	}
}

// formatScalar stringifies a non-array scalar cell value. pgx decodes SQL
// numeric/decimal (and a handful of other types) into pgtype structs that
// have no Stringer, so a bare %v dumps their fields — a numeric prints as
// "{94793049 0 false finite true}" instead of "94793049". Anything that
// implements driver.Valuer is rendered from its driver value (the
// canonical decimal string for numerics); plain Go scalars fall through to
// %v. Keeping this on the driver.Valuer interface keeps the grid package
// driver-agnostic — it never imports pgtype.
func formatScalar(value any) string {
	v, ok := value.(driver.Valuer)
	if !ok {
		return fmt.Sprintf("%v", value)
	}
	dv, err := v.Value()
	if err != nil || dv == nil {
		return fmt.Sprintf("%v", value)
	}
	return fmt.Sprintf("%v", dv)
}

// capCellBytes enforces the MaxCellRenderBytes safety cap (so a huge
// cell can't blow memory) while cutting on a rune boundary, not
// mid-byte — the previous s[:MaxCellRenderBytes-1] slice could split a
// multibyte rune and emit invalid UTF-8. When truncation occurs the
// ellipsis "…" is appended; the retained prefix is the largest rune
// sequence whose byte length is < MaxCellRenderBytes. Cells within the
// cap are returned unchanged.
func capCellBytes(s string) string {
	if len(s) <= MaxCellRenderBytes {
		return s
	}
	// Largest rune boundary < MaxCellRenderBytes (leave headroom for the
	// multibyte ellipsis so the result stays under the byte cap).
	limit := MaxCellRenderBytes - len("…")
	cut := 0
	for i := range s {
		if i > limit {
			break
		}
		cut = i
	}
	return s[:cut] + "…"
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
// is honored independently of foreground colour. Foreground and
// background colour resolution is delegated to the unified theme
// resolver (ColorParamSGR); under NO_COLOR (theme.IsMonochrome) the
// colour params are suppressed while bold/italic/underline are kept.
func sgrPrefixForStyle(s theme.Style) string {
	mono := theme.IsMonochrome()
	var sb strings.Builder
	if !mono {
		if param := theme.ColorParamSGR(s.Fg, theme.Fg); param != "" {
			sb.WriteString("\x1b[" + param + "m")
		}
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
	if !mono {
		if param := theme.ColorParamSGR(s.Bg, theme.Bg); param != "" {
			sb.WriteString("\x1b[" + param + "m")
		}
	}
	return sb.String()
}

// SanitizeCellEscapes strips ANSI escape introducers and C0 control
// characters from s so untrusted server output cannot hijack the
// terminal. Used by renderExpanded, grid cell passthrough, yank, EXPLAIN
// raw text, and exporters.
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

// applySelectionHighlight paints the selected row via ANSI reverse-video.
// Reverse-video is hardcoded (not themed) because gocui's escape interpreter
// doesn't honor 48;5;N cell-background sequences across all terminals;
// swapping fg+bg is universally supported. This is the same trick lazygit
// uses for its selection highlight.
func applySelectionHighlight(decorated string) string {
	return ansiReverseVid + decorated + ansiReset
}
