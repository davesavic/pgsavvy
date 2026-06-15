package controllers

import (
	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// MenuController owns the MENU popup bindings: <CR> to select an item
// (delegates to the menu helper) and <esc> to close the popup.
type MenuController struct {
	baseController
}

// NewMenuController constructs the controller.
func NewMenuController(c *common.Common, core CoreDeps, ui UIDeps) *MenuController {
	return &MenuController{baseController: newBase(c, HelperBag{CoreDeps: core, UIDeps: ui})}
}

// Select activates the cursor-selected menu entry. The MenuPushHelper
// owns the actual selection plumbing (popup state lives in T7b).
func (m *MenuController) Select(_ commands.ExecCtx) error {
	return nil
}

// Close pops the MENU popup.
func (m *MenuController) Close(_ commands.ExecCtx) error {
	if m.helpers.Menu == nil {
		return nil
	}
	err := m.helpers.Menu.PopMenu()
	return m.wrapErr("menu.close", err)
}

// GetKeybindings returns the menu popup bindings.
func (m *MenuController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := m.tr()
	return []*types.ChordBinding{
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEnter}},
			Mode:        types.ModeNormal,
			Scope:       types.MENU,
			ActionID:    commands.MenuConfirm,
			Description: tr.Actions.Confirm,
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEsc}},
			Mode:        types.ModeNormal,
			Scope:       types.MENU,
			ActionID:    commands.MenuCancel,
			Description: tr.Actions.Cancel,
		},
	}
}

// RegisterActions registers menu-specific actions with reg.
func (m *MenuController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	_ = reg.Register(&commands.Command{
		ID:          commands.MenuConfirm,
		Description: "Select menu entry",
		Handler:     m.Select,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.MenuCancel,
		Description: "Close menu",
		Handler:     m.Close,
	})
}

// AttachToContext registers GetKeybindings.
func (m *MenuController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(m.GetKeybindings)
}
