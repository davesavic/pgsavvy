package grid

import (
	"unicode/utf8"

	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/theme"
)

// highlightSpan is a single matched substring to paint inside one cell,
// expressed as BYTE offsets into the cell's renderCellPlain output (the
// same string cellMatch.byteStart/byteEnd index). current marks the span
// the cursor is on — it gets the stronger CurSearch style.
type highlightSpan struct {
	byteStart int
	byteEnd   int
	current   bool
}

// renderCellPaddedHighlighted renders value for col, padded to display
// width w, with the type-aware cell style, then layers the search-match
// highlight onto the matched runes. spans are byte offsets into the plain
// renderCellPlain(value,col) string (the exact string cellMatch indexes).
//
// The base styling/padding mirror renderCellPadded exactly so a cell with
// zero surviving highlight renders byte-identically to the clean path. The
// match highlight is injected on the PADDED visible string by translating
// each span's byte offsets to rune positions, mapping those through
// padRight's rune-count truncation, and wrapping the surviving rune range
// with the Search / CurSearch SGR. When a span is clipped by truncation or
// any offset is out of range the WHOLE rendered cell is highlighted instead
// (whole-cell fallback) so we never slice out of range or mid-rune.
func renderCellPaddedHighlighted(value any, col models.ColumnMeta, w int, isDirty bool, spans []highlightSpan) string {
	visible := renderCellPlain(value, col)
	padded := padRight(visible, w)
	highlighted := applyMatchHighlights(visible, padded, w, spans)
	styled := wrapWithStyle(highlighted, styleForCell(value, col))
	if isDirty {
		styled = wrapWithStyle(styled, dereferenceStyle(theme.Current().DirtyCellBg))
	}
	return styled
}

// applyMatchHighlights wraps the matched rune ranges of padded with the
// search-highlight SGR. visible is the pre-pad plain string the spans index;
// padded is padRight(visible, w). It rebuilds padded rune-by-rune, opening a
// highlight wrapper at each span start and closing it at each span end. A
// span that is clipped by truncation, out of range, or otherwise unmappable
// triggers the whole-cell fallback: the entire padded string is wrapped with
// the (strongest applicable) style.
func applyMatchHighlights(visible, padded string, w int, spans []highlightSpan) string {
	if len(spans) == 0 {
		return padded
	}

	// Translate byte spans into [startRune,endRune) rune ranges in visible.
	// Any malformed span forces the whole-cell fallback.
	runeSpans, ok := byteSpansToRuneSpans(visible, spans)
	if !ok {
		return wholeCellFallback(padded, spans)
	}

	// padRight keeps the first (w-1) runes + "…" when displayWidth(visible) > w.
	// Determine how many of visible's runes survive into padded.
	visibleRunes := utf8.RuneCountInString(visible)
	survivingRunes := visibleRunes
	truncated := visibleRunes > w
	if truncated {
		survivingRunes = w - 1 // the rest is replaced by the single "…" rune
	}

	// If any span reaches into the truncated/ellipsis region, fall back to
	// highlighting the whole rendered cell — we cannot represent a clipped
	// match precisely.
	for _, rs := range runeSpans {
		if rs.endRune > survivingRunes {
			return wholeCellFallback(padded, spans)
		}
	}

	return wrapRuneSpans(padded, runeSpans)
}

// HighlightRuneSpans wraps each [startRune,endRune) half-open RUNE span of s
// in the Search highlight SGR (the same SearchHighlight style the grid search
// path uses) and returns the result. spans index runes (not bytes) into s.
//
// It is robust to malformed input: spans are clamped to [0,len(runes)],
// inverted or empty spans (start>=end after clamping) are dropped, and
// wrapping happens strictly on rune boundaries so it never slices mid-rune or
// panics. An empty spans slice returns s unchanged. Overlapping spans are
// passed through to wrapRuneSpans, which opens/closes per rune index. There is
// a single SGR implementation: this reuses wrapRuneSpans.
func HighlightRuneSpans(s string, spans [][2]int) string {
	if len(spans) == 0 {
		return s
	}

	total := utf8.RuneCountInString(s)
	rspans := make([]runeSpan, 0, len(spans))
	for _, p := range spans {
		start := clampRuneIndex(p[0], total)
		end := clampRuneIndex(p[1], total)
		if start >= end {
			continue
		}
		rspans = append(rspans, runeSpan{startRune: start, endRune: end, current: false})
	}
	if len(rspans) == 0 {
		return s
	}
	return wrapRuneSpans(s, rspans)
}

// clampRuneIndex constrains a rune index to [0,total].
func clampRuneIndex(idx, total int) int {
	if idx < 0 {
		return 0
	}
	if idx > total {
		return total
	}
	return idx
}

// runeSpan is a span translated to rune indices in the plain visible string,
// carrying the current flag through.
type runeSpan struct {
	startRune int
	endRune   int
	current   bool
}

// byteSpansToRuneSpans converts each span's byte offsets into rune indices in
// visible. Offsets are guaranteed on rune boundaries by the matcher; if any
// is out of range or off a boundary the conversion reports !ok so the caller
// can take the whole-cell fallback rather than slice mid-rune.
func byteSpansToRuneSpans(visible string, spans []highlightSpan) ([]runeSpan, bool) {
	out := make([]runeSpan, 0, len(spans))
	for _, s := range spans {
		start, ok := byteOffsetToRuneIndex(visible, s.byteStart)
		if !ok {
			return nil, false
		}
		end, ok := byteOffsetToRuneIndex(visible, s.byteEnd)
		if !ok || end < start {
			return nil, false
		}
		out = append(out, runeSpan{startRune: start, endRune: end, current: s.current})
	}
	return out, true
}

// byteOffsetToRuneIndex returns the number of runes in s that precede byte
// offset off (i.e. the rune index at that offset). off must land on a rune
// boundary within [0,len(s)]; otherwise ok is false.
func byteOffsetToRuneIndex(s string, off int) (int, bool) {
	if off < 0 || off > len(s) {
		return 0, false
	}
	idx := 0
	for i := range s {
		if i == off {
			return idx, true
		}
		if i > off {
			return 0, false // off was inside a multibyte rune
		}
		idx++
	}
	// off == len(s): one past the last rune.
	if off == len(s) {
		return idx, true
	}
	return 0, false
}

// wrapRuneSpans rebuilds padded, wrapping the rune ranges in spans (rune
// indices into the surviving prefix of padded) with the search highlight
// SGR. Non-current spans use SearchHighlight; the current span uses
// CurSearch. Spans are assumed non-overlapping and within the surviving
// runes (verified by the caller).
func wrapRuneSpans(padded string, spans []runeSpan) string {
	// open[runeIdx] => SGR prefix to emit before that rune.
	// close[runeIdx] => true if a reset must follow the rune before it.
	open := make(map[int]string, len(spans))
	closeAt := make(map[int]bool, len(spans))
	for _, s := range spans {
		if s.endRune <= s.startRune {
			continue
		}
		open[s.startRune] = sgrPrefixForStyle(matchStyle(s.current))
		closeAt[s.endRune] = true
	}

	var b []byte
	idx := 0
	for _, r := range padded {
		if closeAt[idx] {
			b = append(b, ansiReset...)
		}
		if pfx, found := open[idx]; found && pfx != "" {
			b = append(b, pfx...)
		}
		b = utf8.AppendRune(b, r)
		idx++
	}
	if closeAt[idx] {
		b = append(b, ansiReset...)
	}
	return string(b)
}

// wholeCellFallback wraps the entire padded cell with a single highlight
// style. The strongest applicable style wins: if any span is the current
// match the cell gets CurSearch, otherwise SearchHighlight.
func wholeCellFallback(padded string, spans []highlightSpan) string {
	current := false
	for _, s := range spans {
		if s.current {
			current = true
			break
		}
	}
	return wrapWithStyle(padded, matchStyle(current))
}

// matchStyle returns the highlight Style for a match span: CurSearch for the
// current match, SearchHighlight otherwise.
func matchStyle(current bool) theme.Style {
	t := theme.Current()
	if current {
		return dereferenceStyle(t.CurSearch)
	}
	return dereferenceStyle(t.SearchHighlight)
}

// cellHighlightSpans collects the highlight spans for cell (r,c) from the
// snapshot's search match list, flagging which (if any) is the current
// match. Returns nil when the search is inactive or the cell has no matches
// so the caller can stay on the byte-identical clean render path.
func cellHighlightSpans(snap viewSnapshot, r, c int) []highlightSpan {
	if !snap.searchActive || len(snap.searchMatches) == 0 {
		return nil
	}
	var cur *cellMatch
	if snap.searchCurrentIdx >= 0 && snap.searchCurrentIdx < len(snap.searchMatches) {
		cur = &snap.searchMatches[snap.searchCurrentIdx]
	}
	var out []highlightSpan
	for i := range snap.searchMatches {
		m := snap.searchMatches[i]
		if m.row != r || m.col != c {
			continue
		}
		out = append(out, highlightSpan{
			byteStart: m.byteStart,
			byteEnd:   m.byteEnd,
			current:   cur != nil && *cur == m,
		})
	}
	return out
}
