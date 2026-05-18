package context

import (
	"strings"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// whichKeyMaxRowWidth is the visible column budget for one popup row
// ("<key>  <description>"). Chosen to fit inside a 40-column popup with
// a single character of right padding.
const whichKeyMaxRowWidth = 38

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
// and rows may be nil at construction (the orchestrator wires them in
// dlp.8c); HandleRender renders nothing in that case.
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

// formatWhichKeyRows renders rows as "key   label" lines. Keys are
// right-padded to the widest key string so labels line up; each line
// is truncated to whichKeyMaxRowWidth to fit the popup.
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
	return b.String()
}
