package controllers

import (
	"fmt"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/grid"
	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

const (
	cellViewerScrollDown   = "cell_viewer.scroll_down"
	cellViewerScrollUp     = "cell_viewer.scroll_up"
	cellViewerHalfPageDown = "cell_viewer.half_page_down"
	cellViewerHalfPageUp   = "cell_viewer.half_page_up"
	cellViewerPageDown     = "cell_viewer.page_down"
	cellViewerPageUp       = "cell_viewer.page_up"
	cellViewerJumpTop      = "cell_viewer.jump_top"
	cellViewerJumpBottom   = "cell_viewer.jump_bottom"
	cellViewerScrollLeft   = "cell_viewer.scroll_left"
	cellViewerScrollRight  = "cell_viewer.scroll_right"
	cellViewerToggleWrap   = "cell_viewer.toggle_wrap"
	cellViewerTogglePretty = "cell_viewer.toggle_pretty"
	cellViewerYank         = "cell_viewer.yank"
	cellViewerEdit         = "cell_viewer.edit"
	cellViewerDismiss      = "cell_viewer.dismiss"
)

// CellViewerController owns the cell-content viewer lifecycle bindings:
//
//   - <leader>gv on RESULT_GRID: Open the full-content viewer for the focused cell.
//   - In-viewer (CELL_VIEWER scope, ModeNormal): j/k scroll, c-d/c-u/c-f/c-b
//     half/full page, g/G jump top/bottom, h/l horizontal scroll (no-wrap only),
//     w wrap toggle, f pretty toggle (JSON only), y yank, i edit-bridge,
//     esc/c-c dismiss.
//
// Concurrency: every handler runs on the gocui MainLoop. No internal locking.
type CellViewerController struct {
	baseController

	ctx        *guicontext.CellViewerContext
	tree       FocusPopper
	picker     GridStatePicker
	clipboard  grid.ClipboardWriter
	cellEditor *CellEditorController
}

// NewCellViewerController constructs the controller. All optional collaborators
// may be nil during unit tests; each handler nil-checks before dispatching.
func NewCellViewerController(
	c *common.Common,
	core CoreDeps,
	ui UIDeps,
	ctx *guicontext.CellViewerContext,
	tree FocusPopper,
	picker GridStatePicker,
) *CellViewerController {
	return &CellViewerController{
		baseController: newBase(c, HelperBag{CoreDeps: core, UIDeps: ui}),
		ctx:            ctx,
		tree:           tree,
		picker:         picker,
	}
}

// SetPicker swaps the GridStatePicker post-construction.
func (v *CellViewerController) SetPicker(p GridStatePicker) { v.picker = p }

// SetTree swaps the FocusPopper post-construction.
func (v *CellViewerController) SetTree(t FocusPopper) { v.tree = t }

// SetClipboard wires the clipboard seam the controller writes yanked text to.
// Nil leaves the controller with no clipboard (yank no-ops).
func (v *CellViewerController) SetClipboard(cb grid.ClipboardWriter) { v.clipboard = cb }

// SetCellEditor wires the CellEditorController peer so the i edit-bridge
// handler can gate through enterDisabled before opening the editor.
func (v *CellViewerController) SetCellEditor(ce *CellEditorController) { v.cellEditor = ce }

// Open is the <leader>gv handler on RESULT_GRID. Snapshots the focused cell,
// opens the viewer context, and pushes it onto the focus stack.
func (v *CellViewerController) Open(_ commands.ExecCtx) error {
	if v.picker == nil || v.ctx == nil || v.tree == nil {
		return nil
	}
	if v.ctx.Active() {
		return nil
	}
	value, col, pk, ok := v.picker.CellSnapshot()
	if !ok {
		return nil
	}
	v.ctx.Open(value, col, pk)
	return v.wrapErr("cell.viewer.open", v.tree.Push(v.ctx))
}

// scrollDown is the j handler.
func (v *CellViewerController) scrollDown(_ commands.ExecCtx) error {
	if v.ctx == nil || !v.ctx.Active() {
		return nil
	}
	v.ctx.Scroll(0, 1)
	return nil
}

// scrollUp is the k handler.
func (v *CellViewerController) scrollUp(_ commands.ExecCtx) error {
	if v.ctx == nil || !v.ctx.Active() {
		return nil
	}
	v.ctx.Scroll(0, -1)
	return nil
}

func (v *CellViewerController) pageSize() int {
	if v.ctx == nil {
		return 10
	}
	return v.ctx.TotalWrappedLines()
}

func (v *CellViewerController) viewHeight() int {
	return 24
}

// halfPageDown is the c-d handler.
func (v *CellViewerController) halfPageDown(_ commands.ExecCtx) error {
	if v.ctx == nil || !v.ctx.Active() {
		return nil
	}
	v.ctx.Scroll(0, v.viewHeight()/2)
	return nil
}

// halfPageUp is the c-u handler.
func (v *CellViewerController) halfPageUp(_ commands.ExecCtx) error {
	if v.ctx == nil || !v.ctx.Active() {
		return nil
	}
	v.ctx.Scroll(0, -(v.viewHeight() / 2))
	return nil
}

// pageDown is the c-f handler.
func (v *CellViewerController) pageDown(_ commands.ExecCtx) error {
	if v.ctx == nil || !v.ctx.Active() {
		return nil
	}
	v.ctx.Scroll(0, v.viewHeight())
	return nil
}

// pageUp is the c-b handler.
func (v *CellViewerController) pageUp(_ commands.ExecCtx) error {
	if v.ctx == nil || !v.ctx.Active() {
		return nil
	}
	v.ctx.Scroll(0, -v.viewHeight())
	return nil
}

// jumpTop is the g (gg) handler.
func (v *CellViewerController) jumpTop(_ commands.ExecCtx) error {
	if v.ctx == nil || !v.ctx.Active() {
		return nil
	}
	v.ctx.SetScrollY(0)
	return nil
}

// jumpBottom is the G handler.
func (v *CellViewerController) jumpBottom(_ commands.ExecCtx) error {
	if v.ctx == nil || !v.ctx.Active() {
		return nil
	}
	total := v.ctx.TotalWrappedLines()
	if total > 0 {
		v.ctx.SetScrollY(total - 1)
	}
	return nil
}

// scrollLeft is the h handler (no-wrap only).
func (v *CellViewerController) scrollLeft(_ commands.ExecCtx) error {
	if v.ctx == nil || !v.ctx.Active() {
		return nil
	}
	if v.ctx.Wrap() {
		return nil
	}
	v.ctx.Scroll(-1, 0)
	return nil
}

// scrollRight is the l handler (no-wrap only).
func (v *CellViewerController) scrollRight(_ commands.ExecCtx) error {
	if v.ctx == nil || !v.ctx.Active() {
		return nil
	}
	if v.ctx.Wrap() {
		return nil
	}
	v.ctx.Scroll(1, 0)
	return nil
}

// toggleWrap is the w handler.
func (v *CellViewerController) toggleWrap(_ commands.ExecCtx) error {
	if v.ctx == nil || !v.ctx.Active() {
		return nil
	}
	v.ctx.ToggleWrap()
	return nil
}

// togglePretty is the f handler. On non-JSON columns, shows a toast and no-ops.
func (v *CellViewerController) togglePretty(_ commands.ExecCtx) error {
	if v.ctx == nil || !v.ctx.Active() {
		return nil
	}
	if !grid.IsJSONColumn(v.ctx.Column()) {
		if v.helpers.Toast != nil {
			v.helpers.Toast.Show("pretty-print only available for json / jsonb columns", bwqToastTTL)
		}
		return nil
	}
	v.ctx.TogglePretty()
	return nil
}

// yank is the y handler. Copies the rendered body (plain text, no ANSI) to the
// clipboard.
func (v *CellViewerController) yank(_ commands.ExecCtx) error {
	if v.ctx == nil || !v.ctx.Active() {
		return nil
	}
	if v.clipboard == nil {
		return nil
	}
	plain := grid.FormatViewerBodyPlain(v.ctx.OriginalValue(), v.ctx.Column(), v.ctx.Pretty())
	if err := v.clipboard.Write(plain); err != nil {
		if v.helpers.Toast != nil {
			v.helpers.Toast.Show("clipboard: "+err.Error(), bwqToastTTL)
		}
		return nil
	}
	if v.helpers.Toast != nil {
		v.helpers.Toast.Show(fmt.Sprintf("yanked (%d bytes)", len(plain)), bwqToastTTL)
	}
	return nil
}

// edit is the i handler. Uses the viewer's captured value/col/pk (NOT a
// re-snapshot from the live grid) and bridges to the cell editor.
func (v *CellViewerController) edit(_ commands.ExecCtx) error {
	if v.ctx == nil || !v.ctx.Active() {
		return nil
	}
	if v.cellEditor == nil {
		return nil
	}
	reason, disabled := v.cellEditor.enterDisabled()
	if disabled {
		if v.helpers.Toast != nil {
			v.helpers.Toast.Show("cell edit: "+reason, bwqToastTTL)
		}
		return nil
	}

	v.ctx.Close()
	if v.tree != nil {
		_ = v.tree.Pop()
	}

	return v.cellEditor.Enter(commands.ExecCtx{})
}

// dismiss is the esc / c-c handler. Closes the viewer context and pops the
// focus stack.
func (v *CellViewerController) dismiss(_ commands.ExecCtx) error {
	if v.ctx == nil || !v.ctx.Active() {
		return nil
	}
	v.ctx.Close()
	if v.tree != nil {
		return v.wrapErr("cell.viewer.dismiss", v.tree.Pop())
	}
	return nil
}

// GetKeybindings returns the chord bindings owned by this controller:
//
//   - <leader>gv on RESULT_GRID (ModeNormal): Open.
//   - In-viewer bindings on CELL_VIEWER (ModeNormal): j/k, c-d/c-u/c-f/c-b,
//     g/G, h/l, w, f, y, i, esc/c-c.
func (v *CellViewerController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := v.tr()
	scope := guicontext.CellViewerKey()
	out := []*types.ChordBinding{
		{
			Sequence:    []types.ChordKey{{Code: 'j'}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    cellViewerScrollDown,
			Description: tr.Actions.CellViewerScrollDown,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'k'}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    cellViewerScrollUp,
			Description: tr.Actions.CellViewerScrollUp,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'd', Mod: types.ChordModCtrl}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    cellViewerHalfPageDown,
			Description: tr.Actions.CellViewerHalfPageDown,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'u', Mod: types.ChordModCtrl}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    cellViewerHalfPageUp,
			Description: tr.Actions.CellViewerHalfPageUp,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'f', Mod: types.ChordModCtrl}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    cellViewerPageDown,
			Description: tr.Actions.CellViewerPageDown,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'b', Mod: types.ChordModCtrl}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    cellViewerPageUp,
			Description: tr.Actions.CellViewerPageUp,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'g'}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    cellViewerJumpTop,
			Description: tr.Actions.CellViewerJumpTop,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'G'}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    cellViewerJumpBottom,
			Description: tr.Actions.CellViewerJumpBottom,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'h'}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    cellViewerScrollLeft,
			Description: tr.Actions.CellViewerScrollLeft,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'l'}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    cellViewerScrollRight,
			Description: tr.Actions.CellViewerScrollRight,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'w'}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    cellViewerToggleWrap,
			Description: tr.Actions.CellViewerToggleWrap,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'f'}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    cellViewerTogglePretty,
			Description: tr.Actions.CellViewerTogglePretty,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'y'}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    cellViewerYank,
			Description: tr.Actions.CellViewerYank,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'i'}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    cellViewerEdit,
			Description: tr.Actions.CellViewerEdit,
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEsc}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    cellViewerDismiss,
			Description: tr.Actions.CellViewerDismiss,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'c', Mod: types.ChordModCtrl}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    cellViewerDismiss,
			Description: tr.Actions.CellViewerDismiss,
		},
	}
	bindings := out
	if seq, err := keys.SequenceFromShorthand("<leader>gv"); err == nil {
		bindings = append(bindings, &types.ChordBinding{
			Sequence:    seq,
			Mode:        types.ModeNormal,
			Scope:       types.RESULT_GRID,
			ActionID:    commands.ResultViewCellOpen,
			Description: tr.Actions.ResultViewCellOpen,
		})
	}
	return bindings
}

// RegisterActions registers all handlers with reg.
func (v *CellViewerController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	tr := v.tr()
	_ = reg.Register(&commands.Command{
		ID:          commands.ResultViewCellOpen,
		Description: tr.Actions.ResultViewCellOpen,
		Tag:         "Result",
		Handler:     v.Open,
		GetDisabled: func(_ commands.ExecCtx) (string, bool) {
			if v.ctx != nil && v.ctx.Active() {
				return "viewer already active", true
			}
			return "", false
		},
	})
	_ = reg.Register(&commands.Command{
		ID:          cellViewerScrollDown,
		Description: tr.Actions.CellViewerScrollDown,
		Tag:         "",
		Handler:     v.scrollDown,
	})
	_ = reg.Register(&commands.Command{
		ID:          cellViewerScrollUp,
		Description: tr.Actions.CellViewerScrollUp,
		Tag:         "",
		Handler:     v.scrollUp,
	})
	_ = reg.Register(&commands.Command{
		ID:          cellViewerHalfPageDown,
		Description: tr.Actions.CellViewerHalfPageDown,
		Tag:         "",
		Handler:     v.halfPageDown,
	})
	_ = reg.Register(&commands.Command{
		ID:          cellViewerHalfPageUp,
		Description: tr.Actions.CellViewerHalfPageUp,
		Tag:         "",
		Handler:     v.halfPageUp,
	})
	_ = reg.Register(&commands.Command{
		ID:          cellViewerPageDown,
		Description: tr.Actions.CellViewerPageDown,
		Tag:         "",
		Handler:     v.pageDown,
	})
	_ = reg.Register(&commands.Command{
		ID:          cellViewerPageUp,
		Description: tr.Actions.CellViewerPageUp,
		Tag:         "",
		Handler:     v.pageUp,
	})
	_ = reg.Register(&commands.Command{
		ID:          cellViewerJumpTop,
		Description: tr.Actions.CellViewerJumpTop,
		Tag:         "",
		Handler:     v.jumpTop,
	})
	_ = reg.Register(&commands.Command{
		ID:          cellViewerJumpBottom,
		Description: tr.Actions.CellViewerJumpBottom,
		Tag:         "",
		Handler:     v.jumpBottom,
	})
	_ = reg.Register(&commands.Command{
		ID:          cellViewerScrollLeft,
		Description: tr.Actions.CellViewerScrollLeft,
		Tag:         "",
		Handler:     v.scrollLeft,
		GetDisabled: func(_ commands.ExecCtx) (string, bool) {
			if v.ctx != nil && v.ctx.Active() && v.ctx.Wrap() {
				return "disabled in wrap mode", true
			}
			return "", false
		},
	})
	_ = reg.Register(&commands.Command{
		ID:          cellViewerScrollRight,
		Description: tr.Actions.CellViewerScrollRight,
		Tag:         "",
		Handler:     v.scrollRight,
		GetDisabled: func(_ commands.ExecCtx) (string, bool) {
			if v.ctx != nil && v.ctx.Active() && v.ctx.Wrap() {
				return "disabled in wrap mode", true
			}
			return "", false
		},
	})
	_ = reg.Register(&commands.Command{
		ID:          cellViewerToggleWrap,
		Description: tr.Actions.CellViewerToggleWrap,
		Tag:         "",
		Handler:     v.toggleWrap,
	})
	_ = reg.Register(&commands.Command{
		ID:          cellViewerTogglePretty,
		Description: tr.Actions.CellViewerTogglePretty,
		Tag:         "",
		Handler:     v.togglePretty,
	})
	_ = reg.Register(&commands.Command{
		ID:          cellViewerYank,
		Description: tr.Actions.CellViewerYank,
		Tag:         "",
		Handler:     v.yank,
	})
	_ = reg.Register(&commands.Command{
		ID:          cellViewerEdit,
		Description: tr.Actions.CellViewerEdit,
		Tag:         "",
		Handler:     v.edit,
		GetDisabled: func(_ commands.ExecCtx) (string, bool) {
			if v.ctx == nil || !v.ctx.Active() {
				return "no active viewer", true
			}
			if v.cellEditor == nil {
				return "editor not wired", true
			}
			return v.cellEditor.enterDisabled()
		},
	})
	_ = reg.Register(&commands.Command{
		ID:          cellViewerDismiss,
		Description: tr.Actions.CellViewerDismiss,
		Tag:         "",
		Handler:     v.dismiss,
	})
}

// AttachToContext registers GetKeybindings on the CELL_VIEWER context.
func (v *CellViewerController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(v.GetKeybindings)
}
