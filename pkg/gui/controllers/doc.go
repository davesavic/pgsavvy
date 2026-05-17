// Package controllers wires keyboard bindings to context state. Each
// concrete controller (connections, schemas, tables, columns, indexes,
// menu, quit) is a thin slice of behaviour that:
//
//  1. Declares a flat []*types.ChordBinding via GetKeybindings.
//  2. Attaches itself to its target context via AttachToContext, which
//     forwards GetKeybindings to context.BaseContext.AddKeybindingsFn.
//
// Controllers never call driver.SetKeybinding directly — every binding
// flows through pkg/gui/keys.RegisterChord (the dlp.8a shim that
// wraps keys.Register for single-key sequences; multi-key dispatch
// lands in dlp.8b/c). The runtime iterates each context's
// GetKeybindings result and calls RegisterChord for every entry.
//
// Helpers from sibling task dbsavvy-zro (T7b) — Confirm, Prompt, Toast,
// OneshotArmer, RefreshHelper, TipHelper, TablesDoubleClickHelper —
// are consumed via narrow interfaces declared in helper_interfaces.go.
// T7b's concrete helper types satisfy those interfaces structurally;
// no controller imports the helpers/ui/ package directly.
//
// Concurrency: all controller handlers run on the gocui MainLoop (D8).
// Background work (driver loads, save debounce) is funneled through
// the helper layer.
package controllers
