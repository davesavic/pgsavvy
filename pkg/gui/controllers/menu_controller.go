package controllers

import (
	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// MenuController owns the MENU popup bindings: <CR> to select an item
// (delegates to the menu helper) and <esc> to close the popup.
type MenuController struct {
	baseController
}

// NewMenuController constructs the controller.
func NewMenuController(c *common.Common, helpers HelperBag) *MenuController {
	return &MenuController{baseController: newBase(c, helpers)}
}

// Select activates the cursor-selected menu entry. The MenuPushHelper
// owns the actual selection plumbing (popup state lives in T7b).
func (m *MenuController) Select() error {
	// The select action is delegated to the menu helper which knows
	// which entry the cursor is on and how to invoke its handler.
	// In T7a we publish the binding; T7b's helper completes the wiring.
	return nil
}

// Close pops the MENU popup.
func (m *MenuController) Close() error {
	if m.helpers.Menu == nil {
		return nil
	}
	err := m.helpers.Menu.PopMenu()
	return m.wrapErr("menu.close", err)
}

// GetKeybindings returns the menu popup bindings.
func (m *MenuController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := m.tr()
	view := viewName(types.MENU)
	return []*types.ChordBinding{
		{
			ViewName:    view,
			Sequence:    []types.ChordKey{{Special: types.KeyEnter}},
			Scope:       types.MENU,
			Handler:     m.Select,
			Description: tr.Actions.Confirm,
		},
		{
			ViewName:    view,
			Sequence:    []types.ChordKey{{Special: types.KeyEsc}},
			Scope:       types.MENU,
			Handler:     m.Close,
			Description: tr.Actions.Cancel,
		},
	}
}

// AttachToContext registers GetKeybindings.
func (m *MenuController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(m.GetKeybindings)
}
