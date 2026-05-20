package controllers

import (
	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// HideOverlayManager is the narrow surface HideOverlayController
// dispatches to. The concrete satisfier is *ui.ResultTabsHelper
// (dbsavvy-uv0.6); the interface keeps the controller package free of
// the helpers/ui import. Mirrors ResultTabsManager's shape so the same
// nil-safe dispatch pattern applies.
type HideOverlayManager interface {
	// HideOverlayMove advances the overlay cursor by d (+1 / -1).
	HideOverlayMove(d int)
	// HideOverlayToggle flips visibility of the column under the cursor.
	// Surfaces ErrMinimumOneVisible as a toast inside the helper.
	HideOverlayToggle()
	// HideOverlayClose applies the overlay's hidden set, pops the
	// overlay context off the focus stack, and clears overlay state.
	HideOverlayClose()
}

// HideOverlayController owns the HIDE_OVERLAY popup bindings opened by
// <leader>gH (dbsavvy-uv0.6). All state lives on the helper; the
// controller is a thin dispatcher into HideOverlayManager.
//
//   - j / <down> / <C-n> move cursor +1
//   - k / <up> / <C-p>  move cursor -1
//   - <space>            toggle the column under the cursor
//   - <esc> / q          apply-and-close (helper handles the pop)
type HideOverlayController struct {
	baseController
	mgr HideOverlayManager
}

// NewHideOverlayController constructs the controller. mgr may be nil
// (test-only); handlers nil-check before dispatching.
func NewHideOverlayController(c *common.Common, helpers HelperBag, mgr HideOverlayManager) *HideOverlayController {
	return &HideOverlayController{baseController: newBase(c, helpers), mgr: mgr}
}

// Up moves the overlay cursor up by one.
func (h *HideOverlayController) Up(_ commands.ExecCtx) error {
	if h.mgr != nil {
		h.mgr.HideOverlayMove(-1)
	}
	return nil
}

// Down moves the overlay cursor down by one.
func (h *HideOverlayController) Down(_ commands.ExecCtx) error {
	if h.mgr != nil {
		h.mgr.HideOverlayMove(1)
	}
	return nil
}

// Toggle flips visibility of the column under the cursor. The helper
// emits a toast if the toggle would leave zero visible columns.
func (h *HideOverlayController) Toggle(_ commands.ExecCtx) error {
	if h.mgr != nil {
		h.mgr.HideOverlayToggle()
	}
	return nil
}

// Close applies the overlay's hidden set and pops the popup.
func (h *HideOverlayController) Close(_ commands.ExecCtx) error {
	if h.mgr != nil {
		h.mgr.HideOverlayClose()
	}
	return nil
}

// GetKeybindings returns the HIDE_OVERLAY-scope bindings.
func (h *HideOverlayController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := h.tr()
	return []*types.ChordBinding{
		{
			Sequence:    []types.ChordKey{{Special: types.KeyDown}},
			Mode:        types.ModeNormal,
			Scope:       types.HIDE_OVERLAY,
			ActionID:    commands.HideOverlayDown,
			Description: tr.Actions.Down,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'j'}},
			Mode:        types.ModeNormal,
			Scope:       types.HIDE_OVERLAY,
			ActionID:    commands.HideOverlayDown,
			Description: tr.Actions.Down,
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyUp}},
			Mode:        types.ModeNormal,
			Scope:       types.HIDE_OVERLAY,
			ActionID:    commands.HideOverlayUp,
			Description: tr.Actions.Up,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'k'}},
			Mode:        types.ModeNormal,
			Scope:       types.HIDE_OVERLAY,
			ActionID:    commands.HideOverlayUp,
			Description: tr.Actions.Up,
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeySpace}},
			Mode:        types.ModeNormal,
			Scope:       types.HIDE_OVERLAY,
			ActionID:    commands.HideOverlayToggle,
			Description: tr.Actions.ResultHideOverlay,
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEsc}},
			Mode:        types.ModeNormal,
			Scope:       types.HIDE_OVERLAY,
			ActionID:    commands.HideOverlayClose,
			Description: tr.Actions.Cancel,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'q'}},
			Mode:        types.ModeNormal,
			Scope:       types.HIDE_OVERLAY,
			ActionID:    commands.HideOverlayClose,
			Description: tr.Actions.Cancel,
		},
	}
}

// RegisterActions registers the up / down / toggle / close handlers.
func (h *HideOverlayController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	_ = reg.Register(&commands.Command{
		ID:          commands.HideOverlayUp,
		Description: "Hide-overlay cursor up",
		Handler:     h.Up,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.HideOverlayDown,
		Description: "Hide-overlay cursor down",
		Handler:     h.Down,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.HideOverlayToggle,
		Description: "Hide-overlay toggle column",
		Handler:     h.Toggle,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.HideOverlayClose,
		Description: "Hide-overlay apply and close",
		Handler:     h.Close,
	})
}

// AttachToContext registers GetKeybindings on the HIDE_OVERLAY context.
func (h *HideOverlayController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(h.GetKeybindings)
}
