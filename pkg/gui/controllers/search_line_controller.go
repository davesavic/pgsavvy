package controllers

import (
	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// SearchLineController owns the SEARCH_LINE TEMPORARY_POPUP <cr> / <esc>
// wiring. The bottom-anchored search input is editable:
// printable runes flow through the master Editor's Passthrough branch
// into v.TextArea (and the WithOnPassthroughEdit seam drives live
// SetSearch). The controller's only roles are translating:
//
//   - <cr> into helper.OnAccept(query) — the search persists, n/N work.
//   - <esc> into helper.OnCancel() — clears the search and restores the
//     pre-search cursor.
//
// The typed query is read from the SEARCH_LINE context's buffer (backed
// by v.TextArea in production).
type SearchLineController struct {
	baseController

	helper *ui.SearchLineHelper
	ctx    *guicontext.SearchLineContext
}

// NewSearchLineController constructs the controller bound to the
// SearchLine helper + concrete context. Either may be nil (test wiring);
// handlers nil-check before dispatching.
func NewSearchLineController(c *common.Common, core CoreDeps, helper *ui.SearchLineHelper, ctx *guicontext.SearchLineContext) *SearchLineController {
	return &SearchLineController{
		baseController: newBase(c, HelperBag{CoreDeps: core}),
		helper:         helper,
		ctx:            ctx,
	}
}

// Accept reads the typed query and hands it to helper.OnAccept (which
// pops the popup; the search stays active).
func (s *SearchLineController) Accept(_ commands.ExecCtx) error {
	if s.helper == nil {
		return nil
	}
	query := ""
	if s.ctx != nil {
		query = s.ctx.ReadAndClearBuffer()
	}
	return s.wrapErr("result.search.accept", s.helper.OnAccept(query))
}

// Cancel drains the typed buffer and hands control to helper.OnCancel
// (which pops the popup and restores the pre-search cursor).
func (s *SearchLineController) Cancel(_ commands.ExecCtx) error {
	if s.helper == nil {
		return nil
	}
	if s.ctx != nil {
		_ = s.ctx.ReadAndClearBuffer()
	}
	return s.wrapErr("result.search.cancel", s.helper.OnCancel())
}

// GetKeybindings returns the SEARCH_LINE-scope bindings: <cr> and <esc>.
// Printable runes / Backspace / arrow keys flow through the master
// Editor's Passthrough branch into gocui.DefaultEditor (v.TextArea), so
// per-key shims are intentionally absent.
func (s *SearchLineController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := s.tr()
	return []*types.ChordBinding{
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEnter}},
			Mode:        types.ModeCommand,
			Scope:       types.SEARCH_LINE,
			ActionID:    commands.ResultSearchAccept,
			Description: tr.Actions.ResultSearchAccept,
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEsc}},
			Mode:        types.ModeCommand,
			Scope:       types.SEARCH_LINE,
			ActionID:    commands.ResultSearchCancel,
			Description: tr.Actions.ResultSearchCancel,
		},
	}
}

// RegisterActions registers the accept / cancel handlers with reg.
func (s *SearchLineController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	_ = reg.Register(&commands.Command{
		ID:          commands.ResultSearchAccept,
		Description: "Accept search",
		Handler:     s.Accept,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ResultSearchCancel,
		Description: "Cancel search",
		Handler:     s.Cancel,
	})
}

// AttachToContext registers GetKeybindings on the SEARCH_LINE context.
func (s *SearchLineController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(s.GetKeybindings)
}
