package context

import (
	"errors"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// depsAlias is the package-local alias for types.ContextTreeDeps. Aliased
// (not redeclared) so the field bag is identical across the boundary;
// downstream tasks add fields to types.ContextTreeDeps without touching
// this file.
type depsAlias = types.ContextTreeDeps

// writeView paints on the caller's UI lane. Worker publications must marshal
// before rendering; scheduling another paint from Layout creates a feedback
// loop, and even content-only queue sends can block MainLoop when saturated.
// A missing view is benign during popup replacement/pre-layout. Other errors
// retain the existing fatal Update delivery rather than disappearing in
// callers that discard HandleRender errors. Nil driver remains a no-op.
func writeView(deps depsAlias, fn func() error) {
	if deps.GuiDriver == nil {
		return
	}
	if err := fn(); err != nil && !errors.Is(err, gocui.ErrUnknownView) {
		deps.GuiDriver.Update(func() error { return err })
	}
}

// railEmptyPlaceholder returns the contextual dim placeholder for an empty
// side rail (SCHEMAS/TABLES/COLUMNS/INDEXES) via deps.RailEmptyText. It is
// nil-safe: when the hook is unset (or returns ""), it returns "" so the
// caller falls through to the prior blank render.
func railEmptyPlaceholder(deps depsAlias, rail types.ContextKey) string {
	if deps.RailEmptyText == nil {
		return ""
	}
	return deps.RailEmptyText(rail)
}

// scrollSideRailIntoView pins the gocui view origin (oy) so the row at
// `cursor` stays inside the visible viewport. Side rails render their
// full item slice into the buffer via SetContent and use a "> " text
// marker for selection; gocui's own scroll origin is independent of
// that marker, so without this call the cursor scrolls off-screen as
// soon as the list overflows while the scrollbar still reads as
// top-pinned. Nil-safe when GuiDriver is unset or the
// view isn't yet realized (unit tests, pre-layout).
//
// Horizontal overflow (names wider than the pane) is handled separately by
// the rail's manual h/l/0/$ pan handlers (controllers.ListControllerTrait),
// which adjust the view's horizontal origin (ox) directly. FocusPoint only
// touches oy, so a user's pan offset survives across renders.
//
// `row` is the BUFFER-LINE index of the selection, not the raw items index:
// rails that skip hidden rows (Schemas) paint the cursor on a compacted line,
// so callers pass RenderedCursorRow(). A negative row (cursor parked on a
// hidden, unpainted row) has no line to focus, so the origin is left as-is.
func scrollSideRailIntoView(deps depsAlias, viewName string, row int) {
	if deps.GuiDriver == nil || row < 0 {
		return
	}
	v, err := deps.GuiDriver.ViewByName(viewName)
	if err != nil || v == nil {
		return
	}
	v.FocusPoint(0, row, true)
}
