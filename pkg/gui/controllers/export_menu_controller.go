package controllers

import (
	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// ExportMenuManager is the narrow surface ExportMenuController dispatches
// to. The concrete satisfier is *ui.ResultTabsHelper; the interface keeps
// the controller package free of the helpers/ui import. Mirrors
// HideOverlayManager's shape so the same nil-safe dispatch pattern
// applies.
type ExportMenuManager interface {
	// ExportMenuMoveField moves the field cursor by d (+1 / -1).
	ExportMenuMoveField(d int)
	// ExportMenuMoveValue adjusts the value of the active field by d.
	ExportMenuMoveValue(d int)
	// ExportMenuConfirm executes the export with the current selection.
	ExportMenuConfirm()
	// ExportMenuCancel discards the menu and pops the popup context.
	ExportMenuCancel()
	// ExportMenuEditPath opens the seeded PROMPT to edit the Path field.
	// No-op unless the Path field is active (File destination).
	ExportMenuEditPath()
}

// ExportMenuController owns the EXPORT_MENU popup bindings opened by
// <leader>oe. All state lives on the helper; the
// controller is a thin dispatcher into ExportMenuManager.
//
//   - j / <down>            move field cursor +1
//   - k / <up>              move field cursor -1
//   - h / <left>            previous value of active field
//   - l / <right>           next value of active field
//   - <cr>                  confirm and start export
//   - <esc> / q             cancel and close the menu
type ExportMenuController struct {
	baseController
	mgr ExportMenuManager
}

// NewExportMenuController constructs the controller. mgr may be nil
// (test-only); handlers nil-check before dispatching.
func NewExportMenuController(c *common.Common, core CoreDeps, mgr ExportMenuManager) *ExportMenuController {
	return &ExportMenuController{baseController: newBase(c, HelperBag{CoreDeps: core}), mgr: mgr}
}

// Up moves the field cursor up by one.
func (e *ExportMenuController) Up(_ commands.ExecCtx) error {
	if e.mgr != nil {
		e.mgr.ExportMenuMoveField(-1)
	}
	return nil
}

// Down moves the field cursor down by one.
func (e *ExportMenuController) Down(_ commands.ExecCtx) error {
	if e.mgr != nil {
		e.mgr.ExportMenuMoveField(1)
	}
	return nil
}

// Left cycles the active field to the previous value.
func (e *ExportMenuController) Left(_ commands.ExecCtx) error {
	if e.mgr != nil {
		e.mgr.ExportMenuMoveValue(-1)
	}
	return nil
}

// Right cycles the active field to the next value.
func (e *ExportMenuController) Right(_ commands.ExecCtx) error {
	if e.mgr != nil {
		e.mgr.ExportMenuMoveValue(1)
	}
	return nil
}

// Confirm starts the export with the current selection.
func (e *ExportMenuController) Confirm(_ commands.ExecCtx) error {
	if e.mgr != nil {
		e.mgr.ExportMenuConfirm()
	}
	return nil
}

// Cancel pops the menu without starting an export.
func (e *ExportMenuController) Cancel(_ commands.ExecCtx) error {
	if e.mgr != nil {
		e.mgr.ExportMenuCancel()
	}
	return nil
}

// EditPath opens the seeded PROMPT to edit the Path field. The manager
// no-ops unless the Path field is active.
func (e *ExportMenuController) EditPath(_ commands.ExecCtx) error {
	if e.mgr != nil {
		e.mgr.ExportMenuEditPath()
	}
	return nil
}

// GetKeybindings returns the EXPORT_MENU-scope bindings.
func (e *ExportMenuController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := e.tr()
	return []*types.ChordBinding{
		{
			Sequence:    []types.ChordKey{{Special: types.KeyUp}},
			Mode:        types.ModeNormal,
			Scope:       types.EXPORT_MENU,
			ActionID:    commands.ExportMenuUp,
			Description: tr.Actions.ExportMenuUp,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'k'}},
			Mode:        types.ModeNormal,
			Scope:       types.EXPORT_MENU,
			ActionID:    commands.ExportMenuUp,
			Description: tr.Actions.ExportMenuUp,
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyDown}},
			Mode:        types.ModeNormal,
			Scope:       types.EXPORT_MENU,
			ActionID:    commands.ExportMenuDown,
			Description: tr.Actions.ExportMenuDown,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'j'}},
			Mode:        types.ModeNormal,
			Scope:       types.EXPORT_MENU,
			ActionID:    commands.ExportMenuDown,
			Description: tr.Actions.ExportMenuDown,
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyLeft}},
			Mode:        types.ModeNormal,
			Scope:       types.EXPORT_MENU,
			ActionID:    commands.ExportMenuLeft,
			Description: tr.Actions.ExportMenuLeft,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'h'}},
			Mode:        types.ModeNormal,
			Scope:       types.EXPORT_MENU,
			ActionID:    commands.ExportMenuLeft,
			Description: tr.Actions.ExportMenuLeft,
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyRight}},
			Mode:        types.ModeNormal,
			Scope:       types.EXPORT_MENU,
			ActionID:    commands.ExportMenuRight,
			Description: tr.Actions.ExportMenuRight,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'l'}},
			Mode:        types.ModeNormal,
			Scope:       types.EXPORT_MENU,
			ActionID:    commands.ExportMenuRight,
			Description: tr.Actions.ExportMenuRight,
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEnter}},
			Mode:        types.ModeNormal,
			Scope:       types.EXPORT_MENU,
			ActionID:    commands.ExportMenuConfirm,
			Description: tr.Actions.ExportMenuConfirm,
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEsc}},
			Mode:        types.ModeNormal,
			Scope:       types.EXPORT_MENU,
			ActionID:    commands.ExportMenuCancel,
			Description: tr.Actions.ExportMenuCancel,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'q'}},
			Mode:        types.ModeNormal,
			Scope:       types.EXPORT_MENU,
			ActionID:    commands.ExportMenuCancel,
			Description: tr.Actions.ExportMenuCancel,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'i'}},
			Mode:        types.ModeNormal,
			Scope:       types.EXPORT_MENU,
			ActionID:    commands.ExportMenuEditPath,
			Description: tr.Actions.ExportMenuEditPath,
		},
	}
}

// RegisterActions registers the up / down / left / right / confirm /
// cancel / confirm-full-with-filter handlers.
func (e *ExportMenuController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	_ = reg.Register(&commands.Command{
		ID:          commands.ExportMenuUp,
		Description: "Export menu cursor up",
		Handler:     e.Up,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ExportMenuDown,
		Description: "Export menu cursor down",
		Handler:     e.Down,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ExportMenuLeft,
		Description: "Export menu previous value",
		Handler:     e.Left,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ExportMenuRight,
		Description: "Export menu next value",
		Handler:     e.Right,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ExportMenuConfirm,
		Description: "Export menu confirm",
		Handler:     e.Confirm,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ExportMenuCancel,
		Description: "Export menu cancel",
		Handler:     e.Cancel,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ExportMenuEditPath,
		Description: "Export menu edit path",
		Handler:     e.EditPath,
	})
}

// AttachToContext registers GetKeybindings on the EXPORT_MENU context.
func (e *ExportMenuController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(e.GetKeybindings)
}
