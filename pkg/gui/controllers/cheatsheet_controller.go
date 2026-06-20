package controllers

import (
	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// Cheatsheet action IDs. Local to the controller —
// the global commands.* table is intentionally not extended here: these
// IDs only ever fire under the CHEATSHEET scope and have no user-facing
// config knob (the bindings are shipped defaults).
const (
	cheatsheetNextTabID  = "cheatsheet.next_tab"
	cheatsheetPrevTabID  = "cheatsheet.prev_tab"
	cheatsheetCloseID    = "cheatsheet.close"
	cheatsheetDownID     = "cheatsheet.scroll_down"
	cheatsheetUpID       = "cheatsheet.scroll_up"
	cheatsheetPageDownID = "cheatsheet.page_down"
	cheatsheetPageUpID   = "cheatsheet.page_up"
	cheatsheetTopID      = "cheatsheet.scroll_top"
	cheatsheetBottomID   = "cheatsheet.scroll_bottom"
)

// cheatsheetHalfPage is the fixed line delta for <c-d>/<c-u>. The popup
// caps at ~30 rows (cheatsheetMaxRows), so a half-page of 10 lines is a
// comfortable jump without needing the live viewport height here.
const cheatsheetHalfPage = 10

// cheatsheetScrollBottom is the sentinel offset for `G`; the layout pass
// clamps it down to the content's last page.
const cheatsheetScrollBottom = 1 << 20

// cheatsheetTree is the narrow focus-stack surface CheatsheetController
// uses to pop the popup off the stack. *gui.ContextTree satisfies it.
type cheatsheetTree interface {
	Pop() error
}

// CheatsheetController owns the CHEATSHEET popup bindings.
// Mirrors TableInspectController's tab-cycling shape:
//
//   - `<tab>` / `]`  cycle to next tab
//   - `[`            cycle to previous tab
//   - `<esc>` / `q`  pop the popup off the focus stack
type CheatsheetController struct {
	baseController
	ctx  *context.CheatsheetContext
	tree cheatsheetTree
}

// NewCheatsheetController constructs a controller. Either dependency may
// be nil during unit tests; handlers nil-check before mutating state.
func NewCheatsheetController(
	c *common.Common,
	core CoreDeps,
	ctx *context.CheatsheetContext,
	tree cheatsheetTree,
) *CheatsheetController {
	return &CheatsheetController{
		baseController: newBase(c, HelperBag{CoreDeps: core}),
		ctx:            ctx,
		tree:           tree,
	}
}

// NextTab advances the active tab with wrap-around. No-op when the context is
// unwired or has no tabs. Per-tab scroll is preserved by the context.
func (h *CheatsheetController) NextTab(_ commands.ExecCtx) error {
	if h.ctx == nil {
		return nil
	}
	h.ctx.NextTab()
	return nil
}

// PrevTab rewinds the active tab with wrap-around. No-op when the context is
// unwired or has no tabs. Per-tab scroll is preserved by the context.
func (h *CheatsheetController) PrevTab(_ commands.ExecCtx) error {
	if h.ctx == nil {
		return nil
	}
	h.ctx.PrevTab()
	return nil
}

// scroll moves the cheatsheet view offset by delta lines. The context
// clamps the top edge; the layout pass clamps the bottom against the
// rendered content height.
func (h *CheatsheetController) scroll(delta int) error {
	if h.ctx != nil {
		h.ctx.Scroll(delta)
	}
	return nil
}

// ScrollDown / ScrollUp move one line; PageDown / PageUp move a half
// page; Top / Bottom jump to the first / last page.
func (h *CheatsheetController) ScrollDown(commands.ExecCtx) error { return h.scroll(1) }
func (h *CheatsheetController) ScrollUp(commands.ExecCtx) error   { return h.scroll(-1) }
func (h *CheatsheetController) PageDown(commands.ExecCtx) error   { return h.scroll(cheatsheetHalfPage) }
func (h *CheatsheetController) PageUp(commands.ExecCtx) error     { return h.scroll(-cheatsheetHalfPage) }

func (h *CheatsheetController) ScrollTop(commands.ExecCtx) error {
	if h.ctx != nil {
		h.ctx.SetScrollY(0)
	}
	return nil
}

func (h *CheatsheetController) ScrollBottom(commands.ExecCtx) error {
	if h.ctx != nil {
		h.ctx.SetScrollY(cheatsheetScrollBottom)
	}
	return nil
}

// Close pops the CHEATSHEET context off the focus stack. No-op when the tree is
// unwired. A subsequent `?` rebuilds the tabs via SetTabs, so no explicit state
// reset is needed here.
func (h *CheatsheetController) Close(_ commands.ExecCtx) error {
	if h.tree == nil {
		return nil
	}
	return h.tree.Pop()
}

// GetKeybindings returns the CHEATSHEET-scope bindings. Five entries:
// <tab>+] cycle next, [ cycle prev, <esc>+q close. All ModeNormal.
func (h *CheatsheetController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	return []*types.ChordBinding{
		{
			Sequence:    []types.ChordKey{{Special: types.KeyTab}},
			Mode:        types.ModeNormal,
			Scope:       types.CHEATSHEET,
			ActionID:    cheatsheetNextTabID,
			Description: "Cheatsheet next tab",
		},
		{
			Sequence:    []types.ChordKey{{Code: ']'}},
			Mode:        types.ModeNormal,
			Scope:       types.CHEATSHEET,
			ActionID:    cheatsheetNextTabID,
			Description: "Cheatsheet next tab",
		},
		{
			Sequence:    []types.ChordKey{{Code: '['}},
			Mode:        types.ModeNormal,
			Scope:       types.CHEATSHEET,
			ActionID:    cheatsheetPrevTabID,
			Description: "Cheatsheet prev tab",
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEsc}},
			Mode:        types.ModeNormal,
			Scope:       types.CHEATSHEET,
			ActionID:    cheatsheetCloseID,
			Description: "Cheatsheet close",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'q'}},
			Mode:        types.ModeNormal,
			Scope:       types.CHEATSHEET,
			ActionID:    cheatsheetCloseID,
			Description: "Cheatsheet close",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'j'}},
			Mode:        types.ModeNormal,
			Scope:       types.CHEATSHEET,
			ActionID:    cheatsheetDownID,
			Description: "Cheatsheet scroll down",
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyDown}},
			Mode:        types.ModeNormal,
			Scope:       types.CHEATSHEET,
			ActionID:    cheatsheetDownID,
			Description: "Cheatsheet scroll down",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'k'}},
			Mode:        types.ModeNormal,
			Scope:       types.CHEATSHEET,
			ActionID:    cheatsheetUpID,
			Description: "Cheatsheet scroll up",
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyUp}},
			Mode:        types.ModeNormal,
			Scope:       types.CHEATSHEET,
			ActionID:    cheatsheetUpID,
			Description: "Cheatsheet scroll up",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'd', Mod: types.ChordModCtrl}},
			Mode:        types.ModeNormal,
			Scope:       types.CHEATSHEET,
			ActionID:    cheatsheetPageDownID,
			Description: "Cheatsheet half-page down",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'u', Mod: types.ChordModCtrl}},
			Mode:        types.ModeNormal,
			Scope:       types.CHEATSHEET,
			ActionID:    cheatsheetPageUpID,
			Description: "Cheatsheet half-page up",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'g'}, {Code: 'g'}},
			Mode:        types.ModeNormal,
			Scope:       types.CHEATSHEET,
			ActionID:    cheatsheetTopID,
			Description: "Cheatsheet scroll to top",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'G'}},
			Mode:        types.ModeNormal,
			Scope:       types.CHEATSHEET,
			ActionID:    cheatsheetBottomID,
			Description: "Cheatsheet scroll to bottom",
		},
	}
}

// RegisterActions registers the cheatsheet tab-cycle + close handlers.
func (h *CheatsheetController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	_ = reg.Register(&commands.Command{
		ID:          cheatsheetNextTabID,
		Description: "Cheatsheet next tab",
		Tag:         "Help",
		Handler:     h.NextTab,
	})
	_ = reg.Register(&commands.Command{
		ID:          cheatsheetPrevTabID,
		Description: "Cheatsheet prev tab",
		Tag:         "Help",
		Handler:     h.PrevTab,
	})
	_ = reg.Register(&commands.Command{
		ID:          cheatsheetCloseID,
		Description: "Cheatsheet close",
		Tag:         "Help",
		Handler:     h.Close,
	})
	for _, b := range []struct {
		id      string
		desc    string
		handler func(commands.ExecCtx) error
	}{
		{cheatsheetDownID, "Cheatsheet scroll down", h.ScrollDown},
		{cheatsheetUpID, "Cheatsheet scroll up", h.ScrollUp},
		{cheatsheetPageDownID, "Cheatsheet half-page down", h.PageDown},
		{cheatsheetPageUpID, "Cheatsheet half-page up", h.PageUp},
		{cheatsheetTopID, "Cheatsheet scroll to top", h.ScrollTop},
		{cheatsheetBottomID, "Cheatsheet scroll to bottom", h.ScrollBottom},
	} {
		_ = reg.Register(&commands.Command{
			ID:          b.id,
			Description: b.desc,
			Tag:         "Help",
			Handler:     b.handler,
		})
	}
}

// AttachToContext registers GetKeybindings on the CHEATSHEET context.
func (h *CheatsheetController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(h.GetKeybindings)
}
