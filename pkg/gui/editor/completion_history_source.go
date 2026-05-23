package editor

import (
	"context"
	"strings"
)

// HistorySourceName is the registered Source.Name() for HistorySource.
const HistorySourceName = "history"

// DefaultHistoryDisplayWidth is the popup-width fallback used to truncate long
// statements in Suggestion.Display. Callers can override via
// HistorySource.DisplayWidth.
const DefaultHistoryDisplayWidth = 80

// DefaultHistoryLimit caps the rows fetched per Suggest call when
// HistorySource.Limit is left zero.
const DefaultHistoryLimit = 20

// historyReturnSymbol is the glyph rendered in place of CR/LF inside a single
// Suggestion.Display line so multi-line statements stay popup-friendly.
const historyReturnSymbol = "⏎"

// HistoryStore is the minimum surface HistorySource needs from query.History.
// Kept as an interface so tests can substitute a fake without spinning up a
// SQLite file.
type HistoryStore interface {
	SearchByPrefix(ctx context.Context, prefix string, limit int) ([]string, error)
}

// HistorySource emits past statements whose FTS5 token stream contains a
// token starting with the identifier prefix at the cursor. Suggestion.Source
// is always HistorySourceName; Suggestion.Text is the full statement (caller
// runs it through SanitizeText before applying) and Suggestion.Display is the
// statement truncated to DisplayWidth with newlines collapsed to a glyph.
//
// HistorySource is the lowest-value source by design — it produces a low
// fixed Score so a colliding keyword or schema suggestion wins the dedupe.
type HistorySource struct {
	Store        HistoryStore
	PriorityVal  int
	Limit        int
	DisplayWidth int
}

// Name implements Source.
func (HistorySource) Name() string { return HistorySourceName }

// Priority implements Source.
func (h HistorySource) Priority() int { return h.PriorityVal }

// Suggest implements Source. Returns an empty slice when Store is nil, when
// the cursor has no identifier prefix, or when the store returns no rows. A
// store error is swallowed (returns empty) — completion is best-effort and
// must not surface backend failures into the editor popup.
func (h HistorySource) Suggest(ctx context.Context, buf *Buffer, pos Position) []Suggestion {
	if h.Store == nil {
		return []Suggestion{}
	}
	prefix := identifierPrefixAt(buf, pos)
	if prefix == "" {
		return []Suggestion{}
	}
	limit := h.Limit
	if limit <= 0 {
		limit = DefaultHistoryLimit
	}
	width := h.DisplayWidth
	if width <= 0 {
		width = DefaultHistoryDisplayWidth
	}

	rows, err := h.Store.SearchByPrefix(ctx, prefix, limit)
	if err != nil || len(rows) == 0 {
		return []Suggestion{}
	}

	out := make([]Suggestion, 0, len(rows))
	for _, stmt := range rows {
		out = append(out, Suggestion{
			Text:    stmt,
			Display: truncateForPopup(stmt, width),
			Source:  HistorySourceName,
			Score:   1,
		})
	}
	return out
}

// truncateForPopup collapses CR/LF runs into a return-arrow glyph and trims
// the result to width runes, appending "…" when truncation happened. width <=
// 0 falls back to DefaultHistoryDisplayWidth so callers cannot ask for an
// empty cell.
func truncateForPopup(s string, width int) string {
	if width <= 0 {
		width = DefaultHistoryDisplayWidth
	}
	// Replace any \r\n pair first so we don't emit two arrows for one line break.
	collapsed := strings.ReplaceAll(s, "\r\n", historyReturnSymbol)
	collapsed = strings.ReplaceAll(collapsed, "\n", historyReturnSymbol)
	collapsed = strings.ReplaceAll(collapsed, "\r", historyReturnSymbol)
	runes := []rune(collapsed)
	if len(runes) <= width {
		return collapsed
	}
	if width <= 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
}
