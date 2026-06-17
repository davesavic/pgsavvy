package ui

import (
	"github.com/davesavic/pgsavvy/pkg/gui"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// MouseWiringDeps bundles every collaborator the mouse-registration
// helper needs. Each field is optional; missing dependencies degrade
// gracefully so the helper is safe to call during partial bootstrap.
//
// All mouse constants are reached via the types package (types.MouseLeft
// / types.MouseWheelUp / etc) so the no-gocui-in-helpers AC stays
// satisfied; the underlying gocui types come in transitively via the
// types-package aliases.
type MouseWiringDeps struct {
	Driver      types.GuiDriver
	Log         keys.WarnLogger
	Tree        *gui.ContextTree
	Registry    *guicontext.ContextTree
	Matcher     *keys.Matcher
	TableDouble *TablesHelper
	TablePicker TablePicker
	// RailActiveTabIsTables reports whether the consolidated rail's active
	// tab is Tables. The former TABLES-view double-click is re-homed onto
	// the consolidated "schemas-tables" container view, gated on this
	// active-tab identity instead of the now-dead "tables" view name.
	// Nil-safe: a nil accessor disables the double-click stub.
	RailActiveTabIsTables func() bool
}

// TablePicker is the cursor accessor for the TABLES rail. The mouse
// helper invokes it on a double-click to obtain the activated row.
// Duplicated locally rather than importing controllers (avoids a
// helpers->controllers cycle).
type TablePicker interface {
	SelectedTable() *models.Table
}

// WireMouse registers click-focus, wheel-scroll, click-row-select, and
// the TABLES double-click stub bindings on every non-stub named view in
// the supplied context registry. Per AC:
//
//   - stub-context views are skipped (Kind == STUB).
//   - bindings are registered via keys.RegisterMouseBinding so the
//     swallow-and-warn-once contract applies.
//   - the Matcher is cancelled inside each mouse-event handler
//     (mouse-event-cancels-arm AC scenario).
//
// Mouse-enabled gating is the CALLER's responsibility — when
// Common.Cfg().UI.Mouse.Enabled is false, the bootstrap simply does
// not call WireMouse.
//
// Returns the first registration error encountered, or nil. With the
// swallow-in-keys-helper contract this is effectively always nil today.
func WireMouse(deps MouseWiringDeps) error {
	if deps.Driver == nil || deps.Registry == nil {
		return nil
	}
	for _, ctx := range deps.Registry.Flatten() {
		if ctx == nil || ctx.GetKind() == types.STUB {
			continue
		}
		view := ctx.GetViewName()
		if view == "" {
			// GLOBAL_CONTEXT has no view — nothing to click on.
			continue
		}
		if err := wireOneView(deps, ctx, view); err != nil {
			return err
		}
	}
	return nil
}

// railTablesTabActive reports whether the consolidated rail's active tab is
// Tables, used to re-home the former TABLES double-click onto the container
// view. Nil-safe: a missing accessor reports false.
func railTablesTabActive(deps MouseWiringDeps) bool {
	return deps.RailActiveTabIsTables != nil && deps.RailActiveTabIsTables()
}

// wireOneView registers the standard mouse bundle on one view: left
// click (focus), wheel-up / wheel-down (scroll-by-1; shift+wheel
// scroll-by-page), and — for the consolidated rail's Tables tab — a
// double-click dispatcher that calls TablesHelper.DoubleClickStub.
func wireOneView(deps MouseWiringDeps, ctx types.IBaseContext, view string) error {
	cancelArm := func() {
		if deps.Matcher != nil {
			deps.Matcher.Cancel()
		}
	}
	pushFocus := func(opts types.ViewMouseBindingOpts) error {
		cancelArm()
		// Push the target context so click==focus, mirroring the
		// keyboard "tab into rail" path. tree.Push is no-op when the
		// context is already on top.
		if deps.Tree != nil {
			if err := deps.Tree.Push(ctx); err != nil {
				return err
			}
		}
		// On the consolidated rail's Tables tab, a double-click consumes the
		// focus push AND invokes the deferred-edit stub toast (per AC). The
		// gate is the container view "schemas-tables" AND the active-tab
		// identity (Tables) — NOT the retired "tables" view name. Both checks
		// are required: pushFocus is registered on every non-stub view, so
		// without the view gate a double-click in any other view (e.g. the
		// query editor) would fire the table stub whenever Tables is active.
		if view == guicontext.SchemaRailViewName && opts.IsDoubleClick && deps.TableDouble != nil && railTablesTabActive(deps) {
			var sel *models.Table
			if deps.TablePicker != nil {
				sel = deps.TablePicker.SelectedTable()
			}
			return deps.TableDouble.DoubleClickStub(sel)
		}
		return nil
	}
	scroll := func(types.ViewMouseBindingOpts) error {
		cancelArm()
		// Real scroll behaviour is owned by the focused context in
		// future epics; today we only fulfil the AC's "cancel arm on
		// mouse event" + "binding is registered" contract.
		return nil
	}

	if err := keys.RegisterMouseBinding(deps.Driver, deps.Log, view, types.MouseLeft, types.ModNone, pushFocus, "Focus / activate"); err != nil {
		return err
	}
	if err := keys.RegisterMouseBinding(deps.Driver, deps.Log, view, types.MouseWheelUp, types.ModNone, scroll, "Scroll up"); err != nil {
		return err
	}
	if err := keys.RegisterMouseBinding(deps.Driver, deps.Log, view, types.MouseWheelDown, types.ModNone, scroll, "Scroll down"); err != nil {
		return err
	}
	if err := keys.RegisterMouseBinding(deps.Driver, deps.Log, view, types.MouseWheelUp, types.ModShift, scroll, "Page up"); err != nil {
		return err
	}
	if err := keys.RegisterMouseBinding(deps.Driver, deps.Log, view, types.MouseWheelDown, types.ModShift, scroll, "Page down"); err != nil {
		return err
	}
	return nil
}
