// Package commands defines the CommandRegistry: the named-action table
// that the keybinding system dispatches into. Keys map to action IDs;
// action IDs resolve to Handlers here. This is the indirection that
// makes the config user-friendly — users rebind by action ID, not by
// closure.
//
// Architectural invariant (DESIGN §10, epic D1): this package MUST
// NOT import any of:
//
//	pkg/gui/keys           (Matcher / ChordTrie are downstream consumers)
//	pkg/gui/controllers    (controllers register INTO this package)
//	pkg/gui/orchestrator   (wires controllers; depends on us)
//	pkg/cheatsheet         (renders from the Registry)
//
// The only allowed dependencies are pkg/gui/types (for Mode and
// ContextKey) and the Go stdlib. An import-cycle CI check enforces this:
//
//	go list -deps ./pkg/gui/commands/... | \
//	    grep -E "pkg/(gui/keys|gui/controllers|gui/orchestrator|cheatsheet)$"
//
// must return empty.
package commands
