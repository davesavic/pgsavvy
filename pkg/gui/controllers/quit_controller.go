package controllers

import (
	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// QuitController owns the global quit + cheatsheet bindings.
//
// Bindings:
//   - <c-c>  : direct quit (returns gocui.ErrQuit)
//   - ?      : open the auto-generated cheatsheet (real impl ships in
//     dlp.10 — today the registered handler is a no-op stub so the
//     binding has a leaf to dispatch).
//
// The <leader>q chord is shipped as a default in
// config.GetDefaultConfig() and routes through the user-config layering
// path at Build time; this controller does not publish it.
//
// QuitController attaches to the GLOBAL_CONTEXT (no view); bindings
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
func (q *QuitController) Quit(_ commands.ExecCtx) error {
	return gocui.ErrQuit
}

// GetKeybindings returns the global quit / cheatsheet bindings.
func (q *QuitController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := q.tr()
	return []*types.ChordBinding{
		{
			Sequence:    []types.ChordKey{{Code: 'c', Mod: types.ChordModCtrl}},
			Mode:        types.ModeNormal,
			Scope:       types.GLOBAL,
			ActionID:    commands.AppQuit,
			Description: tr.Actions.QuitApp,
		},
		{
			Sequence:    []types.ChordKey{{Code: '?'}},
			Mode:        types.ModeNormal,
			Scope:       types.GLOBAL,
			ActionID:    commands.HelpCheatsheet,
			Description: tr.Actions.ShowMenu,
		},
	}
}

// RegisterActions registers the rail-specific action handlers this
// controller owns with reg. Trait actions and rail-switch actions are
// registered once at the Controllers aggregate level.
func (q *QuitController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	_ = reg.Register(&commands.Command{
		ID:          commands.AppQuit,
		Description: "Quit application",
		Handler:     q.Quit,
	})
}

// AttachToContext registers GetKeybindings on the supplied context
// (typically the GLOBAL context).
func (q *QuitController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(q.GetKeybindings)
}
