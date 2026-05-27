package controllers

import (
	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// PlanContextResolver is the narrow surface PlanController uses to find
// the currently-active plan context. The orchestrator wires this to a
// closure that asks ResultTabsHelper.Active() for the focused tab and
// returns its attached *context.PlanContext (or nil for non-plan tabs).
//
// Returning nil is the documented no-op sentinel — every PlanController
// handler nil-checks before dispatching. dbsavvy-uv0.8.
type PlanContextResolver func() *context.PlanContext

// PlanController publishes the EXPLAIN plan-tree keybindings under the
// PLAN scope:
//
//	<CR>    plan.toggle         toggle collapse on the cursor node
//	<C-a>   plan.expand_all     expand every node
//	<C-x>   plan.collapse_all   collapse every interior node except root
//	H       plan.jump_heaviest  jump to heaviest descendant by cost
//	o       plan.toggle_raw     toggle tree ↔ raw-text view
//	j       plan.cursor_down    move cursor down by one visible row
//	k       plan.cursor_up      move cursor up by one visible row
//
// All handlers delegate to the current PlanContext via the resolver;
// when the resolver returns nil (no active plan tab) every handler is a
// no-op. dbsavvy-uv0.8.
type PlanController struct {
	baseController
	resolve PlanContextResolver
}

// NewPlanController constructs the controller. resolve may be nil in
// unit tests that exercise GetKeybindings only.
func NewPlanController(c *common.Common, core CoreDeps, resolve PlanContextResolver) *PlanController {
	return &PlanController{
		baseController: newBase(c, HelperBag{CoreDeps: core}),
		resolve:        resolve,
	}
}

// GetKeybindings returns the seven PLAN-scoped bindings. All run in
// Normal mode; the PLAN context is not editable, so no Insert/Visual
// modes are wired.
//
// Descriptions are English literals today — the i18n.TranslationSet
// does not yet carry Plan.* action labels. When dbsavvy-uv0.8 follow-
// ups add Tr.Actions.Plan* fields, swap each literal for the
// translated string.
func (p *PlanController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	return []*types.ChordBinding{
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEnter}},
			Mode:        types.ModeNormal,
			Scope:       types.PLAN,
			ActionID:    commands.PlanToggle,
			Description: "Toggle collapse on plan node",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'a', Mod: types.ChordModCtrl}},
			Mode:        types.ModeNormal,
			Scope:       types.PLAN,
			ActionID:    commands.PlanExpandAll,
			Description: "Expand every plan node",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'x', Mod: types.ChordModCtrl}},
			Mode:        types.ModeNormal,
			Scope:       types.PLAN,
			ActionID:    commands.PlanCollapseAll,
			Description: "Collapse every plan node except root",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'H'}},
			Mode:        types.ModeNormal,
			Scope:       types.PLAN,
			ActionID:    commands.PlanJumpHeaviest,
			Description: "Jump cursor to heaviest descendant",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'o'}},
			Mode:        types.ModeNormal,
			Scope:       types.PLAN,
			ActionID:    commands.PlanToggleRaw,
			Description: "Toggle plan raw-text view",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'j'}},
			Mode:        types.ModeNormal,
			Scope:       types.PLAN,
			ActionID:    commands.PlanCursorDown,
			Description: "Move plan cursor down",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'k'}},
			Mode:        types.ModeNormal,
			Scope:       types.PLAN,
			ActionID:    commands.PlanCursorUp,
			Description: "Move plan cursor up",
		},
	}
}

// RegisterActions wires the seven handlers to reg.
func (p *PlanController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	type spec struct {
		id          string
		description string
		handler     commands.Handler
	}
	specs := []spec{
		{commands.PlanToggle, "Toggle collapse on plan node", p.handleToggle},
		{commands.PlanExpandAll, "Expand every plan node", p.handleExpandAll},
		{commands.PlanCollapseAll, "Collapse every plan node except root", p.handleCollapseAll},
		{commands.PlanJumpHeaviest, "Jump cursor to heaviest descendant", p.handleJumpHeaviest},
		{commands.PlanToggleRaw, "Toggle plan raw-text view", p.handleToggleRaw},
		{commands.PlanCursorDown, "Move plan cursor down", p.handleCursorDown},
		{commands.PlanCursorUp, "Move plan cursor up", p.handleCursorUp},
	}
	for _, s := range specs {
		_ = reg.Register(&commands.Command{
			ID:          s.id,
			Description: s.description,
			Tag:         "Plan",
			Handler:     s.handler,
		})
	}
}

// AttachToContext registers GetKeybindings on the supplied context.
// The PlanContext per-tab is dynamic (no fixed live context in
// ContextTree) so the orchestrator may pass nil; the bindings still
// reach the trie via AllDefaultBindings.
func (p *PlanController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(p.GetKeybindings)
}

// --- Handlers ---

func (p *PlanController) active() *context.PlanContext {
	if p.resolve == nil {
		return nil
	}
	return p.resolve()
}

func (p *PlanController) handleToggle(_ commands.ExecCtx) error {
	pc := p.active()
	if pc == nil {
		return nil
	}
	pc.Toggle()
	return pc.HandleRender()
}

func (p *PlanController) handleExpandAll(_ commands.ExecCtx) error {
	pc := p.active()
	if pc == nil {
		return nil
	}
	pc.ExpandAll()
	return pc.HandleRender()
}

func (p *PlanController) handleCollapseAll(_ commands.ExecCtx) error {
	pc := p.active()
	if pc == nil {
		return nil
	}
	pc.CollapseAllButRoot()
	return pc.HandleRender()
}

func (p *PlanController) handleJumpHeaviest(_ commands.ExecCtx) error {
	pc := p.active()
	if pc == nil {
		return nil
	}
	pc.JumpHeaviest()
	return pc.HandleRender()
}

func (p *PlanController) handleToggleRaw(_ commands.ExecCtx) error {
	pc := p.active()
	if pc == nil {
		return nil
	}
	pc.ToggleRaw()
	return pc.HandleRender()
}

func (p *PlanController) handleCursorDown(_ commands.ExecCtx) error {
	pc := p.active()
	if pc == nil {
		return nil
	}
	pc.MoveCursor(1)
	return pc.HandleRender()
}

func (p *PlanController) handleCursorUp(_ commands.ExecCtx) error {
	pc := p.active()
	if pc == nil {
		return nil
	}
	pc.MoveCursor(-1)
	return pc.HandleRender()
}
