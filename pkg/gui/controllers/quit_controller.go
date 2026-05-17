package controllers

import (
	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// QuitController owns the global quit / menu bindings:
//   - `q`     : direct quit
//   - `:`     : colon prefix; armed via OneshotArmer.Arm with
//     suffixes={q: quit}, scope=GLOBAL. Any other suffix
//     cancels silently (delegated to OneshotArmer contract).
//   - `?`     : opens the MENU context via MenuPushHelper.
//
// The controller is attached to the GLOBAL_CONTEXT (no view); bindings
// flow through the runtime's global keybinding pass.
type QuitController struct {
	baseController
}

// NewQuitController constructs the controller.
func NewQuitController(c *common.Common, helpers HelperBag) *QuitController {
	return &QuitController{baseController: newBase(c, helpers)}
}

// Quit terminates the gocui main loop synchronously by returning
// gocui.ErrQuit. Per M11g this MUST be invoked from the MainLoop (D8);
// gocui guarantees keystroke handlers run on the MainLoop.
func (q *QuitController) Quit() error {
	return gocui.ErrQuit
}

// ArmColon arms the ":" prefix. The next `q` keystroke invokes Quit;
// any other suffix cancels per OneshotArmer contract. When OneShot is
// not wired (T7a unit tests) the call is a no-op.
func (q *QuitController) ArmColon() error {
	if q.helpers.OneShot == nil {
		return nil
	}
	suffixes := map[rune]func() error{
		'q': q.Quit,
	}
	err := q.helpers.OneShot.Arm(":", suffixes, string(types.GLOBAL))
	return q.wrapErr("quit.arm_colon", err)
}

// ShowMenu opens the MENU popup via the MenuPushHelper.
func (q *QuitController) ShowMenu() error {
	if q.helpers.Menu == nil {
		return nil
	}
	err := q.helpers.Menu.PushMenu()
	return q.wrapErr("quit.show_menu", err)
}

// GetKeybindings returns the global quit/menu bindings.
//
// GLOBAL_CONTEXT has no view, so ViewName is empty (gocui binds
// globally when viewname == "").
func (q *QuitController) GetKeybindings(_ types.KeybindingsOpts) []*types.KeyBinding {
	tr := q.tr()
	const globalView = "" // gocui's "bind globally" sentinel.
	return []*types.KeyBinding{
		{
			ViewName:    globalView,
			Key:         gocui.NewKeyRune('q'),
			Mod:         gocui.ModNone,
			Handler:     q.Quit,
			Description: tr.Actions.QuitApp,
		},
		{
			ViewName:    globalView,
			Key:         gocui.NewKeyRune(':'),
			Mod:         gocui.ModNone,
			Handler:     q.ArmColon,
			Description: tr.Actions.QuitApp,
		},
		{
			ViewName:    globalView,
			Key:         gocui.NewKeyRune('?'),
			Mod:         gocui.ModNone,
			Handler:     q.ShowMenu,
			Description: tr.Actions.ShowMenu,
		},
	}
}

// AttachToContext registers GetKeybindings on the supplied context
// (typically the GLOBAL context).
func (q *QuitController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(q.GetKeybindings)
}
