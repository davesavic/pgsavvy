package controllers

import (
	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// SelectionController owns the SELECTION TEMPORARY_POPUP keybindings
// (dbsavvy-m47.2). Cursor + choices live on the ChoiceHelper; the
// controller is a thin dispatcher that adjusts the cursor on j/k/up/down
// and routes <cr>/<esc> to helper.Submit / helper.Cancel.
//
// j and k are bare-rune aliases for <down> and <up>. The cursor clamps
// at [0, len(choices)-1] inside helper.SetCursor — the controller does
// not duplicate the clamp.
type SelectionController struct {
	baseController
}

// NewSelectionController constructs the controller. Helpers.Choice may
// be nil; every handler nil-checks.
func NewSelectionController(c *common.Common, helpers HelperBag) *SelectionController {
	return &SelectionController{baseController: newBase(c, helpers)}
}

// helper returns the typed ChoiceHelper, casting the interface for
// cursor access. Returns nil when the bag has no Choice helper wired
// or when the wired value does not expose the cursor surface.
type choiceCursor interface {
	Cursor() int
	SetCursor(int)
}

func (s *SelectionController) cursor() choiceCursor {
	if s.helpers.Choice == nil {
		return nil
	}
	if cc, ok := s.helpers.Choice.(choiceCursor); ok {
		return cc
	}
	return nil
}

// Up moves the cursor up by one (helper.SetCursor clamps to 0).
func (s *SelectionController) Up(_ commands.ExecCtx) error {
	cc := s.cursor()
	if cc == nil {
		return nil
	}
	cc.SetCursor(cc.Cursor() - 1)
	return nil
}

// Down moves the cursor down by one (helper.SetCursor clamps to
// len(choices)-1).
func (s *SelectionController) Down(_ commands.ExecCtx) error {
	cc := s.cursor()
	if cc == nil {
		return nil
	}
	cc.SetCursor(cc.Cursor() + 1)
	return nil
}

// Confirm calls helper.Submit with the current cursor and propagates
// the helper's error.
func (s *SelectionController) Confirm(_ commands.ExecCtx) error {
	if s.helpers.Choice == nil {
		return nil
	}
	idx := 0
	if cc := s.cursor(); cc != nil {
		idx = cc.Cursor()
	}
	return s.wrapErr("selection.confirm", s.helpers.Choice.Submit(idx))
}

// Cancel calls helper.Cancel and propagates the helper's error.
func (s *SelectionController) Cancel(_ commands.ExecCtx) error {
	if s.helpers.Choice == nil {
		return nil
	}
	return s.wrapErr("selection.cancel", s.helpers.Choice.Cancel())
}

// GetKeybindings returns the SELECTION-scope bindings: <up>, <down>, k,
// j, <cr>, <esc>.
func (s *SelectionController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := s.tr()
	return []*types.ChordBinding{
		{
			Sequence:    []types.ChordKey{{Special: types.KeyUp}},
			Mode:        types.ModeNormal,
			Scope:       types.SELECTION,
			ActionID:    commands.SelectionUp,
			Description: tr.Actions.Up,
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyDown}},
			Mode:        types.ModeNormal,
			Scope:       types.SELECTION,
			ActionID:    commands.SelectionDown,
			Description: tr.Actions.Down,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'k'}},
			Mode:        types.ModeNormal,
			Scope:       types.SELECTION,
			ActionID:    commands.SelectionUp,
			Description: tr.Actions.Up,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'j'}},
			Mode:        types.ModeNormal,
			Scope:       types.SELECTION,
			ActionID:    commands.SelectionDown,
			Description: tr.Actions.Down,
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEnter}},
			Mode:        types.ModeNormal,
			Scope:       types.SELECTION,
			ActionID:    commands.SelectionConfirm,
			Description: tr.Actions.Confirm,
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEsc}},
			Mode:        types.ModeNormal,
			Scope:       types.SELECTION,
			ActionID:    commands.SelectionCancel,
			Description: tr.Actions.Cancel,
		},
	}
}

// RegisterActions registers the up / down / confirm / cancel handlers.
func (s *SelectionController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	_ = reg.Register(&commands.Command{
		ID:          commands.SelectionUp,
		Description: "Move selection up",
		Handler:     s.Up,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.SelectionDown,
		Description: "Move selection down",
		Handler:     s.Down,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.SelectionConfirm,
		Description: "Confirm selection",
		Handler:     s.Confirm,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.SelectionCancel,
		Description: "Cancel selection",
		Handler:     s.Cancel,
	})
}

// AttachToContext registers GetKeybindings on the SELECTION context.
func (s *SelectionController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(s.GetKeybindings)
}
