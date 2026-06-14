// Package keys is the single call site for registering keyboard and
// mouse bindings with the gocui runtime.
//
// The Register shim is the ONLY function a controller (or any other gui
// collaborator) is permitted to call when wiring a keyboard binding. It
// wraps GuiDriver.SetKeybinding with a uniform logging path so binding
// registration is observable in test recorders and in production logs.
//
// Mouse bindings (RegisterMouseBinding) and the leader / colon next-key
// dispatcher (oneshot_arm.go) also live in this package, so consumers
// depend on a single import for every binding-registration call.
//
// Concurrency invariant (D8): Register MUST be called on the MainLoop
// goroutine. The driver fans out into gocui internals that are not
// safe for concurrent SetKeybinding.
package keys
