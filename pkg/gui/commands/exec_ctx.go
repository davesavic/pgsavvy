package commands

import "github.com/davesavic/dbsavvy/pkg/gui/types"

// ExecCtx carries the per-dispatch context a Handler may need.
//
// Fields are populated by the Matcher (pkg/gui/keys) before invoking
// the Handler. All fields have meaningful zero values so handlers that
// ignore counts/registers/mode/scope still work correctly:
//
//   - Count   == 0  → handlers treat as 1 ("no explicit count given")
//   - Register == 0 → handlers treat as '"' (the default vim register)
//   - Mode    == 0  → ModeNormal (per types.Mode definition)
//   - Scope   == "" → no specific scope (global / unset)
//
// ExecCtx is intentionally a value type, not a pointer. Handlers see
// a snapshot; mutating it cannot leak back into the Matcher.
type ExecCtx struct {
	// Count is the numeric prefix the user typed before the chord
	// (e.g. `5j` → Count=5). Zero means no prefix was given.
	Count int

	// Register is the vim register the user selected with `"x`
	// before the chord (e.g. `"ayy` → Register='a'). Zero means
	// no explicit register; handlers default to '"'.
	Register rune

	// Mode is the active editor mode when the chord fired.
	Mode types.Mode

	// Scope is the ContextKey that owned the dispatch (or "global"
	// for scope-wide bindings).
	Scope types.ContextKey
}
