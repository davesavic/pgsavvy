package controllers

import (
	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/popup"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// Cheatsheet action IDs (dbsavvy-bwq.Z1). Local to the controller —
// the global commands.* table is intentionally not extended here: these
// IDs only ever fire under the CHEATSHEET scope and have no user-facing
// config knob (the bindings are shipped defaults).
const (
	cheatsheetNextTabID = "cheatsheet.next_tab"
	cheatsheetPrevTabID = "cheatsheet.prev_tab"
	cheatsheetCloseID   = "cheatsheet.close"
)

// cheatsheetTree is the narrow focus-stack surface CheatsheetController
// uses to pop the popup off the stack. *gui.ContextTree satisfies it.
type cheatsheetTree interface {
	Pop() error
}

// CheatsheetScopePanel renders one tab of the cheatsheet TabbedPopup —
// the per-scope body produced by the cheatsheet generator. The body is
// captured at construction time; the panel is stateless thereafter.
type CheatsheetScopePanel struct {
	body string
}

// NewCheatsheetScopePanel builds a panel rendering the supplied body.
func NewCheatsheetScopePanel(body string) *CheatsheetScopePanel {
	return &CheatsheetScopePanel{body: body}
}

// Body returns the rendered cheatsheet text for this scope.
func (p *CheatsheetScopePanel) Body() string {
	if p == nil {
		return ""
	}
	return p.body
}

// HandleKey is the popup.Panel side of the contract; this panel does
// not handle keys — the controller owns tab cycling + close.
func (p *CheatsheetScopePanel) HandleKey(types.Key) bool { return false }

// CheatsheetController owns the CHEATSHEET popup bindings
// (dbsavvy-bwq.Z1). Mirrors TableInspectController's tab-cycling shape:
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

// NextTab advances the active tab on the installed TabbedPopup state.
// No-op when the context or state is unwired.
func (h *CheatsheetController) NextTab(_ commands.ExecCtx) error {
	if h.ctx == nil {
		return nil
	}
	if s := h.ctx.State(); s != nil {
		s.NextTab()
	}
	return nil
}

// PrevTab rewinds the active tab on the installed TabbedPopup state.
// No-op when the context or state is unwired.
func (h *CheatsheetController) PrevTab(_ commands.ExecCtx) error {
	if h.ctx == nil {
		return nil
	}
	if s := h.ctx.State(); s != nil {
		s.PrevTab()
	}
	return nil
}

// Close pops the CHEATSHEET context off the focus stack and clears the
// installed state so the next push starts from a fresh tab.
func (h *CheatsheetController) Close(_ commands.ExecCtx) error {
	if h.ctx != nil {
		h.ctx.SetState(nil)
	}
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
}

// AttachToContext registers GetKeybindings on the CHEATSHEET context.
func (h *CheatsheetController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(h.GetKeybindings)
}

// BuildCheatsheetTabs constructs the per-scope TabbedPopup the
// orchestrator pushes when `?` is pressed. The render closure is the
// same body-producing surface CheatsheetContext used pre-Z1; one panel
// is built per scope so the user can tab between the focused scope and
// the global tier without re-pressing `?`.
//
// Tab order:
//
//	[0] focused scope (the ContextKey at the moment `?` was pressed)
//	[1] global tier
//
// When focused == GLOBAL or "" the first tab is dropped so the popup is
// not duplicated.
func BuildCheatsheetTabs(focused types.ContextKey, render func(scope types.ContextKey) string) *popup.TabbedPopup {
	if render == nil {
		return popup.NewTabbedPopup(nil)
	}
	var tabs []popup.Tab
	if focused != "" && focused != types.GLOBAL {
		body := render(focused)
		tabs = append(tabs, popup.Tab{
			Title: string(focused),
			Panel: NewCheatsheetScopePanel(body),
		})
	}
	tabs = append(tabs, popup.Tab{
		Title: "global",
		Panel: NewCheatsheetScopePanel(render(types.GLOBAL)),
	})
	return popup.NewTabbedPopup(tabs)
}
