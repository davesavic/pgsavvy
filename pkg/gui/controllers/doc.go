// Package controllers wires keyboard bindings to context state. Each
// concrete controller (connections, schemas, tables, columns, indexes,
// menu, quit) is a thin slice of behaviour that:
//
//  1. Declares a flat []*types.ChordBinding via GetKeybindings. Bindings
//     carry only the (Sequence, Mode, Scope, ActionID, Description)
//     metadata — there is NO Handler closure on the binding. Action
//     handlers are registered separately via RegisterActions against a
//     commands.Registry.
//  2. Attaches itself to its target context via AttachToContext, which
//     forwards GetKeybindings to context.BaseContext.AddKeybindingsFn.
//  3. Registers its action handlers via RegisterActions. The Controllers
//     aggregate's RegisterActions handles trait + rail-switch actions
//     once so individual controllers don't fight for the same IDs.
//
// Dispatch: chord keystrokes flow through pkg/gui/keys.Matcher (driven
// by a master gocui.Editor for editable views, and by per-root-key
// SetKeybinding shims for non-editable views). The Matcher resolves
// each leaf's ActionID against commands.Registry and invokes the
// registered Handler.
//
// Helpers (Confirm, Prompt, Toast, RefreshHelper, TipHelper,
// TablesDoubleClickHelper, MenuPushHelper) are consumed via narrow
// interfaces declared in helper_interfaces.go.
//
// Concurrency: all controller handlers run on the gocui MainLoop (D8).
// Background work is funneled through the helper layer.
package controllers
