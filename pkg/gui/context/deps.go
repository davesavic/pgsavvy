package context

import (
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// depsAlias is the package-local alias for types.ContextTreeDeps. Aliased
// (not redeclared) so the field bag is identical across the boundary;
// downstream tasks add fields to types.ContextTreeDeps without touching
// this file.
type depsAlias = types.ContextTreeDeps

// writeView runs fn on the driver MainLoop iff deps.GuiDriver is non-nil.
// All concrete contexts that perform view writes go through this helper
// so the nil-driver case (unit tests, partial wiring) is a silent no-op
// rather than a panic.
func writeView(deps depsAlias, fn func() error) {
	if deps.GuiDriver == nil {
		return
	}
	deps.GuiDriver.Update(fn)
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
func scrollSideRailIntoView(deps depsAlias, viewName string, cursor int) {
	if deps.GuiDriver == nil {
		return
	}
	deps.GuiDriver.Update(func() error {
		v, err := deps.GuiDriver.ViewByName(viewName)
		if err != nil || v == nil {
			return nil
		}
		v.FocusPoint(0, cursor, true)
		return nil
	})
}
