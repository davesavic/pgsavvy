package context

import (
	"strings"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// whichKeyMaxRowWidth is the visible column budget for one popup row
// ("<key>  <description>"). Chosen to fit inside a 40-column popup with
// a single character of right padding.
const whichKeyMaxRowWidth = 38

// whichKeyBodyRows is the minimum number of newline-separated rows the
// popup body is padded to when sparse, so the WHICH_KEY view's
// SetContent payload spans the popup's interior height. Without this, a
// body with (say) 3 binding rows produces only 3 lines and the rect's
// remaining rows hold whatever the underlying gocui view layer happened
// to paint last — gocui's clearRunes() handles this in the lazygit fork
// today, but the padding is the orchestrator-level invariant that locks
// the popup-rect = popup-body equivalence regardless of downstream
// changes.
//
// The popup rect height is now content-driven: it grows to
// fit len(rows) rows plus the gocui frame, so the body always covers the
// interior (body lines = max(len(rows), whichKeyBodyRows) >= interior).
// When len(rows) exceeds this floor every row is emitted, so a long
// binding list is no longer clipped.
const whichKeyBodyRows = 10

// WhichKeyContext renders the which-key popup (DISPLAY_CONTEXT). It
// reads visibility + (scope, prefix) from a types.WhichKeyState and
// resolves the rows via a deps.WhichKeyRows closure. Both inputs are
// nil-safe: HandleRender is a silent no-op when either is missing or
// when the popup is hidden.
//
// AddKeybindingsFn is intentionally a no-op — DISPLAY_CONTEXT does not
// accept input bindings (the popup is read-only chrome).
type WhichKeyContext struct {
	BaseContext

	deps     depsAlias
	notifier types.WhichKeyState
	rows     func(scope types.ContextKey, prefix []types.ChordKey) []types.ChildRow
}

// NewWhichKeyContext builds the context bound to WHICH_KEY. notifier
// and rows may be nil at construction (the orchestrator wires them
// in later); HandleRender renders nothing in that case.
func NewWhichKeyContext(
	base BaseContext,
	deps depsAlias,
	notifier types.WhichKeyState,
	rows func(scope types.ContextKey, prefix []types.ChordKey) []types.ChildRow,
) *WhichKeyContext {
	return &WhichKeyContext{
		BaseContext: base,
		deps:        deps,
		notifier:    notifier,
		rows:        rows,
	}
}

// HandleRender writes the formatted popup contents to the WHICH_KEY
// view. No-ops cleanly when the notifier is nil, when the popup is
// hidden, when the rows callback is nil, or when the resolved row set
// is empty.
func (w *WhichKeyContext) HandleRender() error {
	if w.notifier == nil || w.rows == nil {
		return nil
	}
	scope, prefix, visible := w.notifier.Snapshot()
	if !visible {
		return nil
	}
	rows := w.rows(scope, prefix)
	if len(rows) == 0 {
		return nil
	}
	body := formatWhichKeyRows(rows)
	viewName := w.GetViewName()
	writeView(w.deps, func() error {
		return w.deps.GuiDriver.SetContent(viewName, body)
	})
	return nil
}

// AddKeybindingsFn drops the contributor — DISPLAY_CONTEXT views are
// read-only chrome. Overrides BaseContext to make the no-op explicit.
func (w *WhichKeyContext) AddKeybindingsFn(_ types.KeybindingsFn) {}

// Notifier exposes the wired notifier for the orchestrator's layout
// pass (which needs to decide whether to SetView the popup before
// invoking HandleRender). Returns nil when not yet wired.
func (w *WhichKeyContext) Notifier() types.WhichKeyState { return w.notifier }

// SetRows installs the rows-resolver closure post-construction. The
// orchestrator wires the production resolver after the matcher /
// registry is live; tests use this seam to inject a deterministic row
// set so HandleRender renders against the layout pass.
func (w *WhichKeyContext) SetRows(rows func(scope types.ContextKey, prefix []types.ChordKey) []types.ChildRow) {
	w.rows = rows
}

// HasRows reports whether the wired resolver yields at least one row
// for (scope, prefix). The orchestrator's layout pass calls this to
// decide whether to render the popup or DeleteView it for the next
// frame — without this guard, a notifier that flipped Visible() for a
// chord prefix with no children would leave an empty popup rect
// onscreen until the notifier's TTL elapsed. Returns false when the
// resolver is nil so the popup defaults to hidden until wired.
func (w *WhichKeyContext) HasRows(scope types.ContextKey, prefix []types.ChordKey) bool {
	if w.rows == nil {
		return false
	}
	return len(w.rows(scope, prefix)) > 0
}

// RowCount returns the number of children the wired resolver yields for
// (scope, prefix), or 0 when the resolver is nil. The orchestrator's
// layout pass uses it to size the popup rect to fit every binding
// instead of a fixed height that clipped overflow with no scroll.
func (w *WhichKeyContext) RowCount(scope types.ContextKey, prefix []types.ChordKey) int {
	if w.rows == nil {
		return 0
	}
	return len(w.rows(scope, prefix))
}

// formatWhichKeyRows renders rows as "key   label" lines. Keys are
// right-padded to the widest key string so labels line up; each line
// is truncated to whichKeyMaxRowWidth to fit the popup. The output is
// padded with blank trailing lines so it always spans whichKeyBodyRows
// lines (or len(rows) if larger) — see whichKeyBodyRows for rationale.
func formatWhichKeyRows(rows []types.ChildRow) string {
	keyWidth := 0
	for _, r := range rows {
		if l := len(r.Key.String()); l > keyWidth {
			keyWidth = l
		}
	}
	var b strings.Builder
	for i, r := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		key := r.Key.String()
		// keyWidth - len(key) is non-negative by construction.
		pad := strings.Repeat(" ", keyWidth-len(key))
		line := key + pad + "  " + r.Label
		if len(line) > whichKeyMaxRowWidth {
			line = line[:whichKeyMaxRowWidth]
		}
		b.WriteString(line)
	}
	// Bleed-through fix: pad the body with blank lines
	// so the SetContent payload always covers the popup's interior height.
	// gocui's view.draw() / clearRunes() handles this at the cell level
	// in the lazygit fork, but the buffer-level padding is what the
	// orchestrator's regression test asserts — it locks the invariant
	// independent of gocui internals.
	for emitted := len(rows); emitted < whichKeyBodyRows; emitted++ {
		b.WriteByte('\n')
	}
	return b.String()
}
