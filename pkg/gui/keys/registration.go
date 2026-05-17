package keys

import (
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// DebugLogger is the minimal logging surface keys.Register depends on.
// *logrus.Logger satisfies it without further wrapping. Defined locally
// so this package does not import pkg/common just for one method.
type DebugLogger interface {
	Debugf(format string, args ...any)
}

// Register wires a single (view, key, mod, handler) tuple onto the
// supplied GuiDriver. It is the single call site for every keyboard
// binding in the dbsavvy TUI; controllers MUST NOT call
// driver.SetKeybinding directly. Doing so bypasses the uniform debug
// log emitted here and breaks the test-recorder inventory check.
//
// description is the human-readable label used by the bindings menu and
// the options bar. Per M11i it should be sourced from Tr.Actions.*.
//
// driver may be nil during test wiring (controller-level unit tests do
// not need a live driver); in that case the call is a silent no-op so
// the controller-attach test does not need to construct a fake just to
// satisfy this seam.
//
// log may be nil; the call still registers the binding (logging is
// best-effort, not load-bearing).
//
// Errors from driver.SetKeybinding are returned verbatim — the only
// known error class is "view does not exist", which is a wiring bug the
// caller should surface loudly.
func Register(
	driver types.GuiDriver,
	log DebugLogger,
	view string,
	key types.Key,
	mod types.Modifier,
	handler func() error,
	description string,
) error {
	if driver == nil {
		return nil
	}
	if log != nil {
		log.Debugf("keys.Register: view=%q key=%v mod=%v desc=%q", view, key, mod, description)
	}
	return driver.SetKeybinding(view, key, mod, handler)
}
