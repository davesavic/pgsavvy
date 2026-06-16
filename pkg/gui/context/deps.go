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

// writeView runs fn on the driver MainLoop iff deps.GuiDriver is non-nil.
// All concrete contexts that perform view writes go through this helper
// so the nil-driver case (unit tests, partial wiring) is a silent no-op
// rather than a panic.
//
// The fn runs asynchronously on the gocui Update queue, which treats any
// returned error as FATAL and exits MainLoop. A queued view write can race
// the view's lifecycle: a TEMPORARY_POPUP that is re-pushed after being
// evicted (popup-replaces-popup) deletes its gocui view, and the layout
// pass that recreates it may not have run when the queued write drains —
// so the write targets a view that momentarily does not exist and returns
// gocui.ErrUnknownView. That is benign (the layout repaints the view next
// frame), so we swallow it rather than let it kill the app. Any other
// error still propagates.
func writeView(deps depsAlias, fn func() error) {
	if deps.GuiDriver == nil {
		return
	}
	deps.GuiDriver.Update(func() error {
		if err := fn(); err != nil && !errors.Is(err, gocui.ErrUnknownView) {
			return err
		}
		return nil
	})
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
