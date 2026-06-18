package controllers

import (
	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/mattn/go-runewidth"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
)

// railPanStep is how many columns one `h`/`l` press scrolls a side rail
// horizontally. A few columns per press keeps a wide overlap between frames
// (the rail is far wider than this) so nothing in the middle of a long name
// is skipped, while staying responsive; `0`/`$` jump to the name's
// start/end. Terminal key-repeat makes holding `h`/`l` scroll smoothly.
const railPanStep = 4

// railView returns the rail's live *gocui.View, or nil when the driver is
// unwired or the view isn't realized yet (unit tests, pre-layout). types.View
// is an alias for *gocui.View, so the handle exposes the origin/geometry API.
func (l *ListControllerTrait[T]) railView() *gocui.View {
	if l.helpers.Driver == nil {
		return nil
	}
	v, err := l.helpers.Driver.ViewByName(l.railViewName())
	if err != nil {
		return nil
	}
	return v
}

// railViewName is the gocui view the rail physically renders into. It is the
// cursor's (context's) own view name, NOT l.viewName: under the tabbed
// SCHEMA_RAIL / QUERY_RAIL both leaves render into a single consolidated view
// ("schemas-tables" / the query-rail view) while l.viewName stays the leaf's
// dispatch identity ("schemas"/"tables"/…) used for action-ID and scope keys.
// Resolving the scroll target from the context the controller already drives
// keeps the two structurally in lock-step, so a future view rename can't
// silently break panning again. Falls back to l.viewName for cursors that
// don't expose a view (test fakes, nil cursor).
func (l *ListControllerTrait[T]) railViewName() string {
	if c, ok := l.cursor.(interface{ GetViewName() string }); ok {
		if vn := c.GetViewName(); vn != "" {
			return vn
		}
	}
	return l.viewName
}

// cursorIndex is the selected row, or 0 when the cursor is unwired.
func (l *ListControllerTrait[T]) cursorIndex() int {
	if l.cursor == nil {
		return 0
	}
	return l.cursor.Cursor()
}

// renderedCursorRow is the buffer-line index of the selected row. It tracks
// the paint order, which diverges from the raw cursor index on rails that skip
// hidden rows (the Schemas hidden-schema filter): width/scroll lookups index
// BufferLines, so they need the rendered position, not the items index. Rails
// whose cursor doesn't expose a rendered row (test fakes, unfiltered lists)
// fall back to the raw cursor index, which equals the rendered row there.
func (l *ListControllerTrait[T]) renderedCursorRow() int {
	if c, ok := l.cursor.(interface{ RenderedCursorRow() int }); ok {
		return c.RenderedCursorRow()
	}
	return l.cursorIndex()
}

// railRowWidth returns the display width (terminal columns) of the rail row at
// `cursor`, measured from the ANSI-free buffer (gocui decodes color into
// per-cell attributes, so BufferLines carries no escape sequences). 0 when the
// row is out of range.
func railRowWidth(v *gocui.View, cursor int) int {
	lines := v.BufferLines()
	if cursor < 0 || cursor >= len(lines) {
		return 0
	}
	return runewidth.StringWidth(lines[cursor])
}

// railMaxOriginX is the furthest-right horizontal origin worth scrolling to:
// it parks the END of the selected row at the right edge. 0 when the row fits
// the viewport (nothing to scroll).
func railMaxOriginX(v *gocui.View, cursor int) int {
	over := railRowWidth(v, cursor) - v.InnerWidth()
	if over < 0 {
		return 0
	}
	return over
}

// panRail sets the rail's horizontal origin to ox, clamped to
// [0, railMaxOriginX] so the view never scrolls past the selected name's end
// into empty space. SetOriginX floors negatives at 0.
func (l *ListControllerTrait[T]) panRail(ox int) {
	v := l.railView()
	if v == nil {
		return
	}
	if max := railMaxOriginX(v, l.renderedCursorRow()); ox > max {
		ox = max
	}
	v.SetOriginX(ox)
}

// resetPan returns the rail to its left edge. Called on vertical cursor
// movement so each newly-selected row shows from the start of its name rather
// than inheriting the previous row's scroll offset.
func (l *ListControllerTrait[T]) resetPan() {
	if v := l.railView(); v != nil {
		v.SetOriginX(0)
	}
}

// PanLeft scrolls the rail left by railPanStep columns (`h`).
func (l *ListControllerTrait[T]) PanLeft(_ commands.ExecCtx) error {
	v := l.railView()
	if v == nil {
		return nil
	}
	l.panRail(v.OriginX() - railPanStep)
	return nil
}

// PanRight scrolls the rail right by railPanStep columns (`l`).
func (l *ListControllerTrait[T]) PanRight(_ commands.ExecCtx) error {
	v := l.railView()
	if v == nil {
		return nil
	}
	l.panRail(v.OriginX() + railPanStep)
	return nil
}

// PanStart snaps the rail back to the start of the selected name (`0`).
func (l *ListControllerTrait[T]) PanStart(_ commands.ExecCtx) error {
	l.panRail(0)
	return nil
}

// PanEnd snaps the rail to reveal the end of the selected name (`$`).
func (l *ListControllerTrait[T]) PanEnd(_ commands.ExecCtx) error {
	v := l.railView()
	if v == nil {
		return nil
	}
	v.SetOriginX(railMaxOriginX(v, l.renderedCursorRow()))
	return nil
}
