package context

import (
	"strings"

	"github.com/davesavic/pgsavvy/pkg/gui/editor/highlight"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// SavedQueryContextKey aliases types.SAVED_QUERY so existing/forthcoming
// callers can reference a context-package symbol; new code should prefer
// types.SAVED_QUERY directly.
const SavedQueryContextKey = types.SAVED_QUERY

// savedQueryDisplayWidth caps the rendered SQL-preview cell width (in runes)
// before an ellipsis is appended. Matches the history popup default so both
// surfaces truncate consistently.
const savedQueryDisplayWidth = 80

// savedQueryReturnGlyph stands in for collapsed CR/LF runs so multi-line SQL
// renders on one line.
const savedQueryReturnGlyph = "⏎"

// savedQueryEmptyLine is the affordance rendered when there are no saved
// queries so the popup never paints a blank/garbled body.
const savedQueryEmptyLine = "no saved queries yet"

// SavedQueryContext is the non-editable MAIN_CONTEXT leaf of the QUERY_RAIL
// container that browses the named queries persisted in queries.yml. It is a
// thin list state holder modeled on HistoryContext (embeds BaseContext by
// value, owns deps, writes the body via the GuiDriver). It satisfies the
// controllers.SideListCursor surface (Cursor / SetCursor / Items) so the list
// trait can drive j/k/G.
//
// Freshness is load-once + invalidate-on-write: the leaf starts stale, so the
// FIRST tab activation (HandleFocus) fires the injected reload hook; thereafter
// it reloads ONLY after MarkStale (set on a <leader>s save or a dd-delete). It
// never reloads on every focus.
type SavedQueryContext struct {
	BaseContext

	deps Deps

	rows   []models.SavedQuery
	cursor int

	// stale gates the reload-on-focus. Initialized true so the first
	// activation loads. Set true again on a write (MarkStale); cleared after a
	// reload fires.
	stale bool

	// reload is the async load hook injected by the orchestrator (it owns the
	// queries.yml path + threading). Nil-safe: an unwired leaf simply never
	// reloads.
	reload func()
}

// NewSavedQueryContext builds a context bound to the supplied BaseContext
// (the caller sets Key / ViewName / Kind via BaseContextOpts). The leaf starts
// stale so the first activation loads.
func NewSavedQueryContext(base BaseContext, deps Deps) *SavedQueryContext {
	return &SavedQueryContext{BaseContext: base, deps: deps, stale: true}
}

// SetReload injects the async reload hook (orchestrator-owned: it closes over
// the queries.yml path + threading). Nil-safe everywhere it is read.
func (c *SavedQueryContext) SetReload(fn func()) { c.reload = fn }

// MarkStale flags the list for a reload on the next activation. Called on a
// <leader>s save (a new/updated entry was written) and on a dd-delete, so
// switching to the Saved Queries tab afterwards reflects the change.
func (c *SavedQueryContext) MarkStale() { c.stale = true }

// HandleFocus reloads the list ONLY when stale (load-once + invalidate-on-write),
// clearing the flag first so a reload error does not re-arm an endless reload.
// The reload hook is async (worker load → UI-thread refresh).
func (c *SavedQueryContext) HandleFocus(_ types.OnFocusOpts) error {
	if !c.stale {
		return nil
	}
	c.stale = false
	if c.reload != nil {
		c.reload()
	}
	return nil
}

// SetRows loads the saved-query list and resets the cursor to the top.
func (c *SavedQueryContext) SetRows(rows []models.SavedQuery) {
	c.rows = rows
	c.cursor = 0
}

// RefreshRows replaces the list after a destructive edit (dd) WITHOUT
// zeroing the cursor: the cursor is clamped to the new bounds so it lands on
// the row that took the deleted row's slot (or the new last row, or 0 when
// the list emptied). SetRows is deliberately NOT reused here — it snaps the
// cursor to the top, which on a delete would jump the user away from where
// they were working.
func (c *SavedQueryContext) RefreshRows(rows []models.SavedQuery) {
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

// Items satisfies controllers.SideListCursor; returns the rows boxed as
// []any so the generic list trait can drive the cursor uniformly.
func (c *SavedQueryContext) Items() []any {
	out := make([]any, len(c.rows))
	for i := range c.rows {
		out[i] = c.rows[i]
	}
	return out
}

// Cursor returns the current cursor index (0 when empty).
func (c *SavedQueryContext) Cursor() int { return c.cursor }

// SetCursor moves the cursor, clamping into range. An empty list snaps to 0.
func (c *SavedQueryContext) SetCursor(i int) {
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
func (c *SavedQueryContext) Selected() (models.SavedQuery, bool) {
	if len(c.rows) == 0 || c.cursor < 0 || c.cursor >= len(c.rows) {
		return models.SavedQuery{}, false
	}
	return c.rows[c.cursor], true
}

// HandleRender writes one line per row (name + truncated single-line SQL
// preview) with a "> " marker on the cursor row. An empty list renders the
// affordance line.
func (c *SavedQueryContext) HandleRender() error {
	body := formatSavedQueryBody(c.rows, c.cursor)
	viewName := c.GetViewName()
	writeView(c.deps, func() error {
		return c.deps.GuiDriver.SetContent(viewName, body)
	})
	// Pin the gocui scroll origin to the cursor so the selected row stays in
	// view as j/k/G move it past the popup's bottom (mirrors the side rails).
	scrollSideRailIntoView(c.deps, viewName, c.cursor)
	return nil
}

// formatSavedQueryBody composes the popup body. Pure so the render layout is
// testable.
func formatSavedQueryBody(rows []models.SavedQuery, cursor int) string {
	if len(rows) == 0 {
		return savedQueryEmptyLine
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
		b.WriteString(formatSavedQueryRow(r))
	}
	return b.String()
}

// savedQueryNameBold / savedQueryReset wrap the row's name so it reads as the
// label, visually separated from the syntax-highlighted SQL preview. Bold is
// emitted unconditionally (even under NO_COLOR): it is a weight attribute, not
// a colour, and is the only name/SQL distinction left when highlight.Highlight
// falls back to plain text in monochrome.
const (
	savedQueryNameBold = "\x1b[1m"
	savedQueryReset    = "\x1b[0m"
)

// formatSavedQueryRow renders a single row's cell: a bold name followed by a
// collapsed, truncated, syntax-highlighted SQL preview. The SQL is truncated
// BEFORE highlighting so the rune cap never slices an ANSI escape mid-sequence.
func formatSavedQueryRow(r models.SavedQuery) string {
	name := savedQueryNameBold + r.Name + savedQueryReset
	sql := highlight.Highlight(truncateSavedQuerySQL(r.SQL, savedQueryDisplayWidth))
	return name + "  " + sql
}

// truncateSavedQuerySQL collapses CR/LF runs into a return glyph and trims the
// result to width runes, appending an ellipsis when truncation happened.
// width <= 0 falls back to savedQueryDisplayWidth. Mirrors history's
// truncateHistorySQL.
func truncateSavedQuerySQL(s string, width int) string {
	if width <= 0 {
		width = savedQueryDisplayWidth
	}
	collapsed := strings.ReplaceAll(s, "\r\n", savedQueryReturnGlyph)
	collapsed = strings.ReplaceAll(collapsed, "\n", savedQueryReturnGlyph)
	collapsed = strings.ReplaceAll(collapsed, "\r", savedQueryReturnGlyph)
	runes := []rune(collapsed)
	if len(runes) <= width {
		return collapsed
	}
	if width <= 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
}
