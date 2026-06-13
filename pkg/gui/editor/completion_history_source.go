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
// HistorySource is the lowest-value source by design — its Score carries the
// smallest source bias (HistorySourceBias, ko4m.3.2) so a colliding keyword or
// schema suggestion wins the dedupe.
//
// FTS5 (Store.SearchByPrefix) is the PRIMARY retrieval filter; editor.Match is
// applied to the returned rows only to add a quality bump + highlight positions.
// It is NOT a second filter — every FTS row is returned. Rows the matcher
// rejects (ok=false; e.g. the prefix is an FTS token boundary but not a literal
// subsequence of the rendered statement) keep the baseline Score =
// HistorySourceBias with nil Matches; rows it accepts get Score = matchQuality +
// HistorySourceBias and populated Matches.
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
	// History offers whole past statements — useful at a statement start,
	// noise in a schema-completable position (FROM / JOIN / ON / <ident>. /
	// column context) where the relevant tables/columns should lead. Defer
	// to the schema source there and stay quiet. Uses the NARROW structured-
	// context gate (not the broadened AutoTriggerFromContext, which fires on
	// any >=2-rune prefix per dbsavvy-ko4m.6.1 and would suppress history
	// everywhere, e.g. at a bare `SEL` statement start).
	if IsSchemaCompletableContext(buf, pos) {
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
		// Match decorates the FTS row; it never filters it. ok=false →
		// quality 0, nil positions, leaving the baseline bias Score.
		_, quality, positions := Match(prefix, stmt)
		out = append(out, Suggestion{
			Text:    stmt,
			Display: truncateForPopup(stmt, width),
			Source:  HistorySourceName,
			Score:   quality + HistorySourceBias,
			Matches: positions,
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
