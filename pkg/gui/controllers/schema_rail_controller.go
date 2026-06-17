package controllers

import (
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// railLeaf is the per-tab handler surface the SchemaRailController dispatches
// to. Both *SchemasController and *TablesController satisfy it: the cursor /
// pan / confirm methods are promoted from the embedded *ListControllerTrait,
// RefreshRail is defined on each concrete controller. The container picks the
// active leaf by ActiveTab() and forwards the chord to it, so every
// tab-agnostic and per-tab-divergent chord operates on whichever tab is
// visible.
type railLeaf interface {
	Up(commands.ExecCtx) error
	Down(commands.ExecCtx) error
	First(commands.ExecCtx) error
	Last(commands.ExecCtx) error
	PanLeft(commands.ExecCtx) error
	PanRight(commands.ExecCtx) error
	PanStart(commands.ExecCtx) error
	PanEnd(commands.ExecCtx) error
	Confirm(commands.ExecCtx) error
	RefreshRail(commands.ExecCtx) error
}

// SchemaRailController owns EVERY SCHEMA_RAIL keybinding, published exactly
// once under the SCHEMA_RAIL scope (the consolidated "schemas-tables" view).
// It is the single owner so each chord enters the trie once — re-pointing
// both leaf controllers at SCHEMA_RAIL would collide (same scope + sequence,
// different action IDs) or swallow IDs (see DESIGN notes in pgsavvy-i42s.5).
//
// Dispatch model:
//   - tab-agnostic nav (j/k/gg/G/h/l/0/$) → forwarded to the ACTIVE leaf's
//     cursor/pan handler, so it drives whichever tab is visible.
//   - per-tab divergent (<CR>/r) → indexed by ActiveTab() into a 2-entry
//     dispatch table over the two leaves' handlers (no if/else branch).
//   - tab-unique (i / H / U / <leader>H) → guarded: a no-op when the owning
//     tab is not active; otherwise delegates to the leaf / inspect action.
//   - rail search (/ n N <esc>) → published with the existing RailSearch*
//     action IDs; the orchestrator's handlers already resolve the focused
//     rail from ctx.Scope (SCHEMA_RAIL → container.ActiveLeaf()).
//   - tab cycle ([ / ]) → RailTabPrev / RailTabNext: compute the wrapped
//     index and call container.SetActiveTab.
type SchemaRailController struct {
	baseController

	rail    *context.SchemaRailContext
	schemas *SchemasController
	tables  *TablesController
}

// NewSchemaRailController constructs the container controller. rail is the
// live *context.SchemaRailContext (active-tab state); schemas/tables are the
// leaf controllers whose handler methods the container delegates to.
func NewSchemaRailController(
	base baseController,
	rail *context.SchemaRailContext,
	schemas *SchemasController,
	tables *TablesController,
) *SchemaRailController {
	return &SchemaRailController{
		baseController: base,
		rail:           rail,
		schemas:        schemas,
		tables:         tables,
	}
}

// activeTab returns the container's active-tab index, defaulting to the
// Schemas tab when the rail is unwired (test wiring).
func (s *SchemaRailController) activeTab() int {
	if s.rail == nil {
		return context.SchemaRailTabSchemas
	}
	return s.rail.ActiveTab()
}

// activeLeaf returns the railLeaf for the active tab, or nil when the leaf is
// unwired. Indexed by ActiveTab() — no if/else.
func (s *SchemaRailController) activeLeaf() railLeaf {
	leaves := [2]railLeaf{s.schemas, s.tables}
	leaf := leaves[s.activeTab()]
	// A typed-nil *SchemasController stored in railLeaf is non-nil at the
	// interface level; the concrete handlers nil-check their own state, so a
	// nil leaf would still dispatch. Guard explicitly instead.
	switch s.activeTab() {
	case context.SchemaRailTabTables:
		if s.tables == nil {
			return nil
		}
	default:
		if s.schemas == nil {
			return nil
		}
	}
	return leaf
}

// dispatchActive forwards a chord to the active leaf's handler. fn selects the
// railLeaf method; a nil active leaf is a no-op.
func (s *SchemaRailController) dispatchActive(ctx commands.ExecCtx, fn func(railLeaf) func(commands.ExecCtx) error) error {
	leaf := s.activeLeaf()
	if leaf == nil {
		return nil
	}
	return fn(leaf)(ctx)
}

// --- tab-agnostic nav: drive the active leaf's cursor/pan. ---

func (s *SchemaRailController) up(ctx commands.ExecCtx) error {
	return s.dispatchActive(ctx, func(l railLeaf) func(commands.ExecCtx) error { return l.Up })
}

func (s *SchemaRailController) down(ctx commands.ExecCtx) error {
	return s.dispatchActive(ctx, func(l railLeaf) func(commands.ExecCtx) error { return l.Down })
}

func (s *SchemaRailController) first(ctx commands.ExecCtx) error {
	return s.dispatchActive(ctx, func(l railLeaf) func(commands.ExecCtx) error { return l.First })
}

func (s *SchemaRailController) last(ctx commands.ExecCtx) error {
	return s.dispatchActive(ctx, func(l railLeaf) func(commands.ExecCtx) error { return l.Last })
}

func (s *SchemaRailController) panLeft(ctx commands.ExecCtx) error {
	return s.dispatchActive(ctx, func(l railLeaf) func(commands.ExecCtx) error { return l.PanLeft })
}

func (s *SchemaRailController) panRight(ctx commands.ExecCtx) error {
	return s.dispatchActive(ctx, func(l railLeaf) func(commands.ExecCtx) error { return l.PanRight })
}

func (s *SchemaRailController) panStart(ctx commands.ExecCtx) error {
	return s.dispatchActive(ctx, func(l railLeaf) func(commands.ExecCtx) error { return l.PanStart })
}

func (s *SchemaRailController) panEnd(ctx commands.ExecCtx) error {
	return s.dispatchActive(ctx, func(l railLeaf) func(commands.ExecCtx) error { return l.PanEnd })
}

// --- per-tab divergent: <CR> and r dispatch by ActiveTab. ---

func (s *SchemaRailController) confirm(ctx commands.ExecCtx) error {
	return s.dispatchActive(ctx, func(l railLeaf) func(commands.ExecCtx) error { return l.Confirm })
}

func (s *SchemaRailController) refresh(ctx commands.ExecCtx) error {
	return s.dispatchActive(ctx, func(l railLeaf) func(commands.ExecCtx) error { return l.RefreshRail })
}

// --- tab-unique: guarded by the owning tab. ---

// inspect (`i`) is a Tables-tab chord: it opens the TABLE_INSPECT popup for
// the selected table. No-op on the Schemas tab. The handler is owned by the
// orchestrator (it needs the focus tree + connect invoker); we forward to it
// via the registry captured at RegisterActions time.
func (s *SchemaRailController) inspect(reg *commands.Registry) commands.Handler {
	return func(ctx commands.ExecCtx) error {
		if s.activeTab() != context.SchemaRailTabTables || reg == nil {
			return nil
		}
		cmd, ok := reg.Get(commands.TableInspectOpen)
		if !ok || cmd == nil || cmd.Handler == nil {
			return nil
		}
		return cmd.Handler(ctx)
	}
}

func (s *SchemaRailController) hide(ctx commands.ExecCtx) error {
	if s.activeTab() != context.SchemaRailTabSchemas || s.schemas == nil {
		return nil
	}
	return s.schemas.HideSchema(ctx)
}

func (s *SchemaRailController) unhide(ctx commands.ExecCtx) error {
	if s.activeTab() != context.SchemaRailTabSchemas || s.schemas == nil {
		return nil
	}
	return s.schemas.UnhideSchema(ctx)
}

func (s *SchemaRailController) toggleShowHidden(ctx commands.ExecCtx) error {
	if s.activeTab() != context.SchemaRailTabSchemas || s.schemas == nil {
		return nil
	}
	return s.schemas.ToggleShowHidden(ctx)
}

// --- tab cycle: '[' / ']' with edge-wrap. ---

const schemaRailTabCount = context.SchemaRailTabTables - context.SchemaRailTabSchemas + 1

// tabNext (`]`) advances to the next tab, wrapping past the last back to the
// first. tabPrev (`[`) is the mirror. Both compute the wrapped index then call
// SetActiveTab. Modular arithmetic over schemaRailTabCount gives the wrap
// without branching on the edge.
func (s *SchemaRailController) tabNext(commands.ExecCtx) error {
	if s.rail == nil {
		return nil
	}
	s.rail.SetActiveTab((s.rail.ActiveTab() + 1) % schemaRailTabCount)
	return nil
}

func (s *SchemaRailController) tabPrev(commands.ExecCtx) error {
	if s.rail == nil {
		return nil
	}
	s.rail.SetActiveTab((s.rail.ActiveTab() - 1 + schemaRailTabCount) % schemaRailTabCount)
	return nil
}

// GetKeybindings returns every SCHEMA_RAIL binding, each published once.
func (s *SchemaRailController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := s.tr()
	scope := types.SCHEMA_RAIL

	bind := func(seq []types.ChordKey, id, desc string, showInBar bool) *types.ChordBinding {
		return &types.ChordBinding{
			Sequence:    seq,
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    id,
			Description: desc,
			ShowInBar:   showInBar,
		}
	}

	out := []*types.ChordBinding{
		// tab-agnostic nav.
		bind([]types.ChordKey{{Code: 'j'}}, commands.SchemaRailDown, tr.Actions.Down, false),
		bind([]types.ChordKey{{Code: 'k'}}, commands.SchemaRailUp, tr.Actions.Up, false),
		bind([]types.ChordKey{{Code: 'g'}, {Code: 'g'}}, commands.SchemaRailJumpFirst, tr.Actions.JumpFirst, false),
		bind([]types.ChordKey{{Code: 'G'}}, commands.SchemaRailJumpLast, tr.Actions.JumpLast, false),
		bind([]types.ChordKey{{Code: 'h'}}, commands.SchemaRailPanLeft, tr.Actions.PanLeft, false),
		bind([]types.ChordKey{{Code: 'l'}}, commands.SchemaRailPanRight, tr.Actions.PanRight, false),
		bind([]types.ChordKey{{Code: '0'}}, commands.SchemaRailPanStart, tr.Actions.PanStart, false),
		bind([]types.ChordKey{{Code: '$'}}, commands.SchemaRailPanEnd, tr.Actions.PanEnd, false),

		// per-tab divergent.
		bind([]types.ChordKey{{Special: types.KeyEnter}}, commands.SchemaRailConfirm, tr.Actions.Confirm, true),
		bind([]types.ChordKey{{Code: 'r'}}, commands.SchemaRailRefresh, tr.Actions.RefreshRail, false),

		// tab-unique.
		bind([]types.ChordKey{{Code: 'i'}}, commands.SchemaRailInspect, tr.Actions.TableInspectOpen, true),
		bind([]types.ChordKey{{Code: 'H'}}, commands.SchemaRailHide, tr.Actions.HideSchema, false),
		bind([]types.ChordKey{{Code: 'U'}}, commands.SchemaRailUnhide, tr.Actions.UnhideSchema, false),

		// rail search (orchestrator-owned, scope-aware handlers).
		bind([]types.ChordKey{{Code: '/'}}, commands.RailSearchPrompt, tr.Actions.RailSearchPrompt, false),
		bind([]types.ChordKey{{Code: 'n'}}, commands.RailSearchNext, tr.Actions.RailSearchNext, false),
		bind([]types.ChordKey{{Code: 'N'}}, commands.RailSearchPrev, tr.Actions.RailSearchPrev, false),
		bind([]types.ChordKey{{Special: types.KeyEsc}}, commands.RailSearchClear, tr.Actions.RailSearchClear, false),

		// tab cycle (edge-wrapping).
		bind([]types.ChordKey{{Code: ']'}}, commands.RailTabNext, tr.Actions.RailTabNext, true),
		bind([]types.ChordKey{{Code: '['}}, commands.RailTabPrev, tr.Actions.RailTabPrev, true),

		// Ctrl+L escape into the QueryEditor (+ no-op fall-throughs handled
		// by railDirectionalBindings for the SCHEMA_RAIL scope).
	}
	out = append(out, railDirectionalBindings(scope, tr)...)

	// <leader>H toggle-show-hidden (Schemas tab only).
	if seq, err := keys.SequenceFromShorthand("<leader>H"); err == nil {
		out = append(out, bind(seq, commands.SchemaRailToggleShowHidden, tr.Actions.ToggleShowHidden, false))
	}
	return out
}

// RegisterActions registers every SCHEMA_RAIL action handler the container
// owns. The RailSwitch* / RailSearch* IDs the bindings also reference are
// registered elsewhere (RegisterRailSwitchActions / the orchestrator).
func (s *SchemaRailController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	register := func(id, desc string, h commands.Handler) {
		_ = reg.Register(&commands.Command{ID: id, Description: desc, Handler: h})
	}

	register(commands.SchemaRailUp, "Rail cursor up", s.up)
	register(commands.SchemaRailDown, "Rail cursor down", s.down)
	register(commands.SchemaRailJumpFirst, "Rail jump to first", s.first)
	register(commands.SchemaRailJumpLast, "Rail jump to last", s.last)
	register(commands.SchemaRailPanLeft, "Rail scroll left", s.panLeft)
	register(commands.SchemaRailPanRight, "Rail scroll right", s.panRight)
	register(commands.SchemaRailPanStart, "Rail scroll to start", s.panStart)
	register(commands.SchemaRailPanEnd, "Rail scroll to end", s.panEnd)
	register(commands.SchemaRailConfirm, "Activate rail row", s.confirm)
	register(commands.SchemaRailRefresh, "Refresh rail", s.refresh)
	register(commands.SchemaRailInspect, "Inspect table", s.inspect(reg))
	register(commands.SchemaRailHide, "Hide schema", s.hide)
	register(commands.SchemaRailUnhide, "Unhide schema", s.unhide)
	register(commands.SchemaRailToggleShowHidden, "Toggle show-hidden schemas", s.toggleShowHidden)
	register(commands.RailTabNext, "Next rail tab", s.tabNext)
	register(commands.RailTabPrev, "Previous rail tab", s.tabPrev)
}

// AttachToContext subscribes GetKeybindings to the SCHEMA_RAIL container.
func (s *SchemaRailController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(s.GetKeybindings)
}
