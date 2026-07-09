package controllers

import (
	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// FilePickerContextResolver resolves the currently-active FilePickerContext.
// The orchestrator wires this to return tree.FilePicker or nil.
type FilePickerContextResolver func() *context.FilePickerContext

// FilePickerController publishes the FILE_PICKER popup bindings.
//
//   - j / k         cursor down / up
//   - gg / G        jump to first / last entry
//   - Enter         descend into dir or confirm selection
//   - h / Backspace ascend to parent directory
//   - q / Esc       cancel and dismiss
//   - /             activate search
//   - n (search)    next match
//   - N (search)    prev match
//   - .             toggle hidden files
//   - a             create new directory
//   - s             cycle sort order
//   - H             navigate to home directory
//   - Tab           focus filename input (save mode only)
type FilePickerController struct {
	baseController
	resolve FilePickerContextResolver
}

// NewFilePickerController constructs the controller. resolve may be nil
// (test-only); handlers nil-check before dispatching.
func NewFilePickerController(c *common.Common, core CoreDeps, ui UIDeps, resolve FilePickerContextResolver) *FilePickerController {
	return &FilePickerController{
		baseController: newBase(c, HelperBag{CoreDeps: core, UIDeps: ui}),
		resolve:        resolve,
	}
}

func (f *FilePickerController) active() *context.FilePickerContext {
	if f.resolve == nil {
		return nil
	}
	return f.resolve()
}

// GetKeybindings returns the FILE_PICKER-scoped bindings.
func (f *FilePickerController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	return []*types.ChordBinding{
		{
			Sequence:    []types.ChordKey{{Code: 'j'}},
			Mode:        types.ModeCommand,
			Scope:       types.FILE_PICKER,
			ActionID:    commands.FilePickerDown,
			Description: "Move cursor down",
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyDown}},
			Mode:        types.ModeCommand,
			Scope:       types.FILE_PICKER,
			ActionID:    commands.FilePickerDown,
			Description: "Move cursor down",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'k'}},
			Mode:        types.ModeCommand,
			Scope:       types.FILE_PICKER,
			ActionID:    commands.FilePickerUp,
			Description: "Move cursor up",
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyUp}},
			Mode:        types.ModeCommand,
			Scope:       types.FILE_PICKER,
			ActionID:    commands.FilePickerUp,
			Description: "Move cursor up",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'g'}, {Code: 'g'}},
			Mode:        types.ModeCommand,
			Scope:       types.FILE_PICKER,
			ActionID:    commands.FilePickerJumpFirst,
			Description: "Jump to first entry",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'G'}},
			Mode:        types.ModeCommand,
			Scope:       types.FILE_PICKER,
			ActionID:    commands.FilePickerJumpLast,
			Description: "Jump to last entry",
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEnter}},
			Mode:        types.ModeCommand,
			Scope:       types.FILE_PICKER,
			ActionID:    commands.FilePickerConfirm,
			Description: "Descend into directory / confirm selection",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'h'}},
			Mode:        types.ModeCommand,
			Scope:       types.FILE_PICKER,
			ActionID:    commands.FilePickerAscend,
			Description: "Ascend to parent directory",
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyBs}},
			Mode:        types.ModeCommand,
			Scope:       types.FILE_PICKER,
			ActionID:    commands.FilePickerAscend,
			Description: "Ascend to parent directory",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'i'}},
			Mode:        types.ModeCommand,
			Scope:       types.FILE_PICKER,
			ActionID:    commands.FilePickerFocusInput,
			Description: "Edit filename",
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyTab}},
			Mode:        types.ModeCommand,
			Scope:       types.FILE_PICKER,
			ActionID:    commands.FilePickerFocusInput,
			Description: "Edit filename",
		},
		// --- ModeInsert bindings (active while typing filename) ---
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEnter}},
			Mode:        types.ModeInsert,
			Scope:       types.FILE_PICKER,
			ActionID:    commands.FilePickerConfirm,
			Description: "Confirm filename",
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEsc}},
			Mode:        types.ModeInsert,
			Scope:       types.FILE_PICKER,
			ActionID:    commands.FilePickerFocusInput,
			Description: "Return to file listing",
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEsc}},
			Mode:        types.ModeCommand,
			Scope:       types.FILE_PICKER,
			ActionID:    commands.FilePickerCancel,
			Description: "Cancel file picker",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'q'}},
			Mode:        types.ModeCommand,
			Scope:       types.FILE_PICKER,
			ActionID:    commands.FilePickerCancel,
			Description: "Cancel file picker",
		},
		{
			Sequence:    []types.ChordKey{{Code: '/'}},
			Mode:        types.ModeCommand,
			Scope:       types.FILE_PICKER,
			ActionID:    commands.FilePickerSearch,
			Description: "Search entries",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'n'}},
			Mode:        types.ModeCommand,
			Scope:       types.FILE_PICKER,
			ActionID:    commands.FilePickerSearchNext,
			Description: "Next search match",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'N'}},
			Mode:        types.ModeCommand,
			Scope:       types.FILE_PICKER,
			ActionID:    commands.FilePickerSearchPrev,
			Description: "Previous search match",
		},
		{
			Sequence:    []types.ChordKey{{Code: '.'}},
			Mode:        types.ModeCommand,
			Scope:       types.FILE_PICKER,
			ActionID:    commands.FilePickerHidden,
			Description: "Toggle hidden files",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'a'}},
			Mode:        types.ModeCommand,
			Scope:       types.FILE_PICKER,
			ActionID:    commands.FilePickerNewDir,
			Description: "Create new directory",
		},
		{
			Sequence:    []types.ChordKey{{Code: 's'}},
			Mode:        types.ModeCommand,
			Scope:       types.FILE_PICKER,
			ActionID:    commands.FilePickerSort,
			Description: "Cycle sort order",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'H'}},
			Mode:        types.ModeCommand,
			Scope:       types.FILE_PICKER,
			ActionID:    commands.FilePickerHome,
			Description: "Navigate to home directory",
		},
	}
}

// RegisterActions wires the handlers to reg.
func (f *FilePickerController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	type spec struct {
		id          string
		description string
		handler     commands.Handler
	}
	specs := []spec{
		{commands.FilePickerUp, "Move cursor up", f.handleUp},
		{commands.FilePickerDown, "Move cursor down", f.handleDown},
		{commands.FilePickerJumpFirst, "Jump to first entry", f.handleJumpFirst},
		{commands.FilePickerJumpLast, "Jump to last entry", f.handleJumpLast},
		{commands.FilePickerConfirm, "Confirm selection", f.handleConfirm},
		{commands.FilePickerAscend, "Ascend to parent", f.handleAscend},
		{commands.FilePickerCancel, "Cancel picker", f.handleCancel},
		{commands.FilePickerSearch, "Search entries", f.handleSearch},
		{commands.FilePickerSearchNext, "Next search match", f.handleSearchNext},
		{commands.FilePickerSearchPrev, "Prev search match", f.handleSearchPrev},
		{commands.FilePickerHidden, "Toggle hidden files", f.handleHidden},
		{commands.FilePickerNewDir, "Create new directory", f.handleNewDir},
		{commands.FilePickerSort, "Cycle sort order", f.handleSort},
		{commands.FilePickerHome, "Navigate to home", f.handleHome},
		{commands.FilePickerFocusInput, "Focus filename input", f.handleFocusInput},
	}
	for _, s := range specs {
		_ = reg.Register(&commands.Command{
			ID:          s.id,
			Description: s.description,
			Tag:         "File Picker",
			Handler:     s.handler,
		})
	}
}

// AttachToContext registers GetKeybindings on the FILE_PICKER context.
func (f *FilePickerController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(f.GetKeybindings)
}

// --- Handlers ---

func (f *FilePickerController) handleUp(_ commands.ExecCtx) error {
	fp := f.active()
	if fp == nil {
		return nil
	}
	if fp.InputFocused() {
		return nil
	}
	fp.MoveCursor(-1)
	return fp.HandleRender()
}

func (f *FilePickerController) handleDown(_ commands.ExecCtx) error {
	fp := f.active()
	if fp == nil {
		return nil
	}
	if fp.InputFocused() {
		return nil
	}
	fp.MoveCursor(1)
	return fp.HandleRender()
}

func (f *FilePickerController) handleJumpFirst(_ commands.ExecCtx) error {
	fp := f.active()
	if fp == nil {
		return nil
	}
	if fp.InputFocused() {
		fp.SetCursor(0)
		return fp.HandleRender()
	}
	fp.SetCursor(0)
	return fp.HandleRender()
}

func (f *FilePickerController) handleJumpLast(_ commands.ExecCtx) error {
	fp := f.active()
	if fp == nil {
		return nil
	}
	if fp.InputFocused() {
		return nil
	}
	fp.SetCursor(len(fp.Items()) - 1)
	return fp.HandleRender()
}

func (f *FilePickerController) handleConfirm(_ commands.ExecCtx) error {
	fp := f.active()
	if fp == nil {
		return nil
	}
	if fp.InputFocused() {
		switch {
		case fp.SearchInputActive():
			fp.ApplySearch()
			return fp.HandleRender()
		case fp.NewDirInputActive():
			fp.ApplyNewDir()
			return fp.HandleRender()
		}
		fp.Confirm()
		return nil
	}
	sel := fp.Selected()
	if sel.IsDir {
		fp.Descend()
		return fp.HandleRender()
	}
	fp.Confirm()
	return nil
}

func (f *FilePickerController) handleAscend(_ commands.ExecCtx) error {
	fp := f.active()
	if fp == nil {
		return nil
	}
	if fp.InputFocused() {
		return nil
	}
	fp.Ascend()
	return fp.HandleRender()
}

func (f *FilePickerController) handleCancel(_ commands.ExecCtx) error {
	fp := f.active()
	if fp == nil {
		return nil
	}
	fp.Cancel()
	return nil
}

func (f *FilePickerController) handleSearch(_ commands.ExecCtx) error {
	fp := f.active()
	if fp == nil {
		return nil
	}
	fp.ActivateSearch()
	return fp.HandleRender()
}

func (f *FilePickerController) handleSearchNext(_ commands.ExecCtx) error {
	fp := f.active()
	if fp == nil {
		return nil
	}
	fp.NextMatch()
	return fp.HandleRender()
}

func (f *FilePickerController) handleSearchPrev(_ commands.ExecCtx) error {
	fp := f.active()
	if fp == nil {
		return nil
	}
	fp.PrevMatch()
	return fp.HandleRender()
}

func (f *FilePickerController) handleHidden(_ commands.ExecCtx) error {
	fp := f.active()
	if fp == nil {
		return nil
	}
	fp.ToggleHidden()
	return fp.HandleRender()
}

func (f *FilePickerController) handleNewDir(_ commands.ExecCtx) error {
	fp := f.active()
	if fp == nil {
		return nil
	}
	fp.ActivateNewDir()
	return fp.HandleRender()
}

func (f *FilePickerController) handleFocusInput(_ commands.ExecCtx) error {
	fp := f.active()
	if fp == nil {
		return nil
	}
	switch {
	case fp.SearchInputActive():
		fp.CancelSearch()
	case fp.NewDirInputActive():
		fp.CancelNewDir()
	default:
		fp.ToggleInputFocus()
	}
	return fp.HandleRender()
}

func (f *FilePickerController) handleSort(_ commands.ExecCtx) error {
	fp := f.active()
	if fp == nil {
		return nil
	}
	fp.CycleSort()
	return fp.HandleRender()
}

func (f *FilePickerController) handleHome(_ commands.ExecCtx) error {
	fp := f.active()
	if fp == nil {
		return nil
	}
	fp.NavigateHome()
	return fp.HandleRender()
}
