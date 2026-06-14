package keys

import (
	"sync"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// WarnLogger is the minimal logging surface the mouse helper uses to
// record the single "mouse mode unsupported" warning per session.
// *slog.Logger satisfies it; tests can supply a recorder.
type WarnLogger interface {
	Warn(msg string, args ...any)
}

// mouseWarnOnce is the package-level guard ensuring the "mouse mode
// unsupported" warning is emitted at most once per process. The TUI may
// re-register mouse bindings on hot-reload; without this gate a noisy
// terminal would spam the log.
var mouseWarnOnce sync.Once

// RegisterMouseBinding wires (view, mouseKey, mod, handler) onto the
// supplied GuiDriver using SetViewClickBinding. The handler executes on
// the gocui MainLoop (gocui guarantees this for click bindings).
//
// Defensive contract:
//   - A nil driver is a silent no-op (mirrors keys.Register so the
//     controller wiring path stays uniform).
//   - SetViewClickBinding errors are SWALLOWED and logged once per
//     process via the supplied WarnLogger. The plan calls this out as
//     "unsupported mouse mode" defense: this gocui fork never returns
//     such an error today, but if a future runtime does, the TUI must
//     not refuse to start.
//   - description is currently unused by the gocui surface (the menu /
//     cheatsheet builders pull labels from i18n.Actions), but is kept in
//     the signature for symmetry with keys.Register and for future
//     opt-in instrumentation.
//
// No gocui import in this package — the binding is
// constructed via the types-package aliases.
func RegisterMouseBinding(
	driver types.GuiDriver,
	log WarnLogger,
	view string,
	key types.KeyName,
	mod types.Modifier,
	handler func(types.ViewMouseBindingOpts) error,
	description string,
) error {
	if driver == nil {
		return nil
	}
	_ = description // see doc comment
	binding := &types.ViewMouseBinding{
		ViewName: view,
		Key:      key,
		Modifier: mod,
		Handler:  handler,
	}
	if err := driver.SetViewClickBinding(binding); err != nil {
		// Per AC: log once, swallow always — the TUI must remain usable
		// when the terminal refuses to enter mouse mode.
		if log != nil {
			mouseWarnOnce.Do(func() {
				log.Warn("mouse: SetViewClickBinding failed (mouse mode may be unsupported by this terminal)", "err", err)
			})
		}
		return nil
	}
	return nil
}

// ResetMouseWarnOnceForTest re-arms the package-level warn-once guard so
// tests can exercise the "first error logs" path repeatedly within one
// process. NOT for production use.
func ResetMouseWarnOnceForTest() {
	mouseWarnOnce = sync.Once{}
}
