package context

import (
	"fmt"
	"strings"
	"time"

	"github.com/davesavic/pgsavvy/pkg/gui/editor/highlight"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/query"
)

// HistoryContextKey aliases types.HISTORY so existing/forthcoming callers
// can reference a context-package symbol; new code should prefer
// types.HISTORY directly.
const HistoryContextKey = types.HISTORY

// historyDisplayWidth caps the rendered SQL cell width (in runes) before an
// ellipsis is appended. Matches the editor popup default so both history
// surfaces truncate consistently.
const historyDisplayWidth = 80

// historyReturnGlyph stands in for collapsed CR/LF runs so multi-line SQL
// renders on one line.
const historyReturnGlyph = "⏎"

// historyOKGlyph / historyFailGlyph mark per-row success. The presentation
// package only carries tree glyphs (▼▶─), so these are net-new and local.
const (
	historyOKGlyph   = "✓"
	historyFailGlyph = "✗"
)

// historyEmptyLine is the affordance rendered when there is no history so
// the popup never paints a blank/garbled body.
const historyEmptyLine = "no query history yet"

// HistoryContext is the non-editable MAIN_CONTEXT leaf of the QUERY_RAIL
// container that browses a window of recent query-history rows. It is a thin
// list state holder modeled on FKReversePickerContext (embeds BaseContext by
// value, owns deps, writes the body via the GuiDriver). It satisfies the
// controllers.SideListCursor surface (Cursor / SetCursor / Items) so the list
// trait can drive j/k/G.
//
// Each activation requests a refresh so external CLI inserts become visible.
// The orchestrator coalesces requests and owns the read lifecycle.
type HistoryContext struct {
	BaseContext

	deps Deps

	rows   []query.HistoryRow
	cursor int

	// reload is the async load hook injected by the orchestrator (it owns the
	// history store + threading). Nil-safe: an unwired leaf simply never
	// reloads.
	reload func()
}

// NewHistoryContext builds a context bound to the supplied BaseContext (the
// caller sets Key / ViewName / Kind via BaseContextOpts).
func NewHistoryContext(base BaseContext, deps Deps) *HistoryContext {
	return &HistoryContext{BaseContext: base, deps: deps}
}

// SetReload injects the async reload hook (orchestrator-owned: it closes over
// the history store + threading). Nil-safe everywhere it is read.
func (c *HistoryContext) SetReload(fn func()) { c.reload = fn }

// MarkStale preserves the write-notification API; every activation now reloads.
func (c *HistoryContext) MarkStale() {}

// HandleFocus requests an asynchronous, single-flight refresh on every entry.
func (c *HistoryContext) HandleFocus(_ types.OnFocusOpts) error {
	if c.reload != nil {
		c.reload()
	}
	return nil
}

// RefreshRows replaces the list WITHOUT zeroing the cursor: the cursor is
// clamped to the new bounds so a background reload landing never jumps the user
// to the top. Mirrors SavedQueryContext.RefreshRows.
func (c *HistoryContext) RefreshRows(rows []query.HistoryRow) {
	c.rows = rows
	if len(rows) == 0 {
		c.cursor = 0
		return
	}
	if c.cursor >= len(rows) {
		c.cursor = len(rows) - 1
	}
	if c.cursor < 0 {
		c.cursor = 0
	}
}

// SetRows loads the window (caller supplies it newest-first) and resets the
// cursor to the top.
func (c *HistoryContext) SetRows(rows []query.HistoryRow) {
	c.rows = rows
	c.cursor = 0
}

// Items satisfies controllers.SideListCursor; returns the rows boxed as
// []any so the generic list trait can drive the cursor uniformly.
func (c *HistoryContext) Items() []any {
	out := make([]any, len(c.rows))
	for i := range c.rows {
		out[i] = c.rows[i]
	}
	return out
}

// Cursor returns the current cursor index (0 when empty).
func (c *HistoryContext) Cursor() int { return c.cursor }

// SetCursor moves the cursor, clamping into range. An empty list snaps to 0.
func (c *HistoryContext) SetCursor(i int) {
	if len(c.rows) == 0 {
		c.cursor = 0
		return
	}
	if i < 0 {
		i = 0
	}
	if i >= len(c.rows) {
		i = len(c.rows) - 1
	}
	c.cursor = i
}

// Selected returns the row under the cursor. The bool is false when the
// list is empty or the cursor is out of range.
func (c *HistoryContext) Selected() (query.HistoryRow, bool) {
	if len(c.rows) == 0 || c.cursor < 0 || c.cursor >= len(c.rows) {
		return query.HistoryRow{}, false
	}
	return c.rows[c.cursor], true
}

// HandleRender writes one line per row (truncated single-line SQL +
// relative time + success glyph + duration) with a "> " marker on the
// cursor row. An empty window renders the affordance line.
func (c *HistoryContext) HandleRender() error {
	body := formatHistoryBody(c.rows, c.cursor, time.Now())
	viewName := c.GetViewName()
	writeView(c.deps, func() error {
		return c.deps.GuiDriver.SetContent(viewName, body)
	})
	// Pin the gocui scroll origin to the cursor so the selected row stays in
	// view as j/k/G move it past the popup's bottom (mirrors the side rails).
	scrollSideRailIntoView(c.deps, viewName, c.cursor)
	return nil
}

// formatHistoryBody composes the popup body. Pure (now is injected) so the
// render layout is testable without the wall clock.
func formatHistoryBody(rows []query.HistoryRow, cursor int, now time.Time) string {
	if len(rows) == 0 {
		return historyEmptyLine
	}

	var b strings.Builder
	for i, r := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		if i == cursor {
			b.WriteString("> ")
		} else {
			b.WriteString("  ")
		}
		b.WriteString(formatHistoryRow(r, now))
	}
	return b.String()
}

// Labels precede SQL so narrow layouts retain textual source/outcome. Sanitize
// before highlighting/truncation; original rows remain unchanged for recall.
func formatHistoryRow(r query.HistoryRow, now time.Time) string {
	source, outcome := r.Source, r.Outcome
	if source == "" {
		source = "tui"
	}
	if outcome == "" {
		outcome = "failed"
		if r.Succeeded {
			outcome = "succeeded"
		}
	}
	profile := truncateHistorySQL(r.ConnectionID, 24)
	if profile == "" {
		profile = "-"
	}
	return fmt.Sprintf(
		"[%s %s] %s | %s %s %s  %s",
		truncateHistorySQL(source, 12), truncateHistorySQL(outcome, 12), profile,
		highlight.Highlight(truncateHistorySQL(r.SQL, historyDisplayWidth)),
		historyGlyph(outcome == "succeeded"),
		formatRelativeTime(now, time.UnixMilli(r.ExecutedAt)),
		formatHistoryDuration(r.DurationMS),
	)
}

// truncateHistorySQL collapses CR/LF runs into a return glyph and trims the
// result to width runes, appending an ellipsis when truncation happened.
// width <= 0 falls back to historyDisplayWidth. Mirrors the editor's
// truncateForPopup (kept local; that helper is unexported in package
// editor).
func truncateHistorySQL(s string, width int) string {
	if width <= 0 {
		width = historyDisplayWidth
	}
	collapsed := sanitizeHistoryText(s)
	runes := []rune(collapsed)
	if len(runes) <= width {
		return collapsed
	}
	if width <= 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
}

func sanitizeHistoryText(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	runes := []rune(s)
	var out strings.Builder
	controlString, csi := false, false
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if controlString {
			if r == '\a' || r == '\u009c' {
				controlString = false
			} else if r == '\x1b' && i+1 < len(runes) && runes[i+1] == '\\' {
				controlString = false
				i++
			}
			continue
		}
		if csi {
			if r >= 0x40 && r <= 0x7e {
				csi = false
			}
			continue
		}
		if r == '\x1b' && i+1 < len(runes) {
			switch runes[i+1] {
			case ']', 'P', 'X', '^', '_':
				controlString = true
				i++
			case '[':
				csi = true
				i++
			}
			continue
		}
		switch {
		case r == '\u009d' || r == '\u0090' || r == '\u0098' || r == '\u009e' || r == '\u009f':
			controlString = true
		case r == '\u009b':
			csi = true
		case r == '\n' || r == '\u2028' || r == '\u2029':
			out.WriteString(historyReturnGlyph)
		case r == '\t':
			out.WriteByte(' ')
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

// historyGlyph maps row success to a check/cross glyph.
func historyGlyph(succeeded bool) string {
	if succeeded {
		return historyOKGlyph
	}
	return historyFailGlyph
}

// formatRelativeTime renders then relative to now in coarse buckets
// ("just now", "3m ago", "2h ago", "5d ago"). A future or sub-minute delta
// is "just now".
func formatRelativeTime(now, then time.Time) string {
	d := now.Sub(then)
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
}

// formatHistoryDuration renders a millisecond duration human-readably:
// sub-second as "<n>ms", otherwise as "<n.n>s".
func formatHistoryDuration(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}
