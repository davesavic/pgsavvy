package controllers

import (
	"github.com/davesavic/pgsavvy/pkg/gui"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/i18n"
)

// attachable is the slice of context.BaseContext every controller calls.
// We do NOT depend on pkg/gui/context here — the BaseContext type
// already satisfies this interface via its AddKeybindingsFn method.
type attachable interface {
	AddKeybindingsFn(fn types.KeybindingsFn)
}

// viewName converts a ContextKey into its underlying view-name string.
// Centralised so the cast site is grep-able when T8 starts renaming
// view slots.
func viewName(k types.ContextKey) string { return string(k) }

// railSwitchBindings returns the digit-3/4 + <tab> bindings the
// QUERY_EDITOR / RESULT_GRID / PLAN panes register so the user can hop
// between the QueryEditor / results pane (and cycle) from any of them.
// The SCHEMA_RAIL container does NOT use this helper — it owns its own
// bindings via SchemaRailController (digits '1'/'2' were dropped in
// pgsavvy-i42s.5; the rail is reached via Ctrl+H / <tab> instead).
// The handlers are registered ONCE per process via
// RegisterRailSwitchActions; the per-controller bindings just publish
// ActionID strings the Matcher resolves through the commands.Registry.
//
// It also appends per-view directional Ctrl+H/J/K/L chords.
// The right-hand main column is split vertically (boxlayout.ROW puts
// children top-to-bottom), so QueryEditor sits ABOVE Results — the
// j/k axis covers that edge, not h/l:
//
//   - on QUERY_EDITOR: Ctrl+H = SCHEMA_RAIL, Ctrl+J = active
//     result tab. Mode defaults to ModeNormal so Insert mode keeps
//     Ctrl+H = Backspace and Ctrl+J = LineFeed via gocui DefaultEditor.
//   - on RESULT_GRID / PLAN: Ctrl+H = SCHEMA_RAIL (the rail is left of
//     the whole main column, not just the editor), Ctrl+K = QueryEditor.
func railSwitchBindings(view string, tr *i18n.TranslationSet) []*types.ChordBinding {
	scope := types.ContextKey(view)
	out := []*types.ChordBinding{
		{
			Sequence:    []types.ChordKey{{Code: '3'}},
			Scope:       scope,
			ActionID:    commands.RailSwitchQueryEditor,
			Description: tr.Actions.RailQueryEditor,
		},
		{
			Sequence:    []types.ChordKey{{Code: '4'}},
			Scope:       scope,
			ActionID:    commands.RailSwitchResults,
			Description: tr.Actions.RailResults,
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyTab}},
			Scope:       scope,
			ActionID:    commands.RailSwitchNext,
			Description: tr.Actions.RailQueryEditor,
		},
	}
	out = append(out, railDirectionalBindings(scope, tr)...)
	return out
}

// railDirectionalBindings returns the Ctrl+H/J/K/L chord bindings for
// the given scope. The mapping depends on which pane this view is:
// vertically-stacked side rail, QueryEditor, or RESULT_GRID. Any other
// scope returns nil — directional navigation is only published on
// panes the focus model knows about.
func railDirectionalBindings(scope types.ContextKey, tr *i18n.TranslationSet) []*types.ChordBinding {
	ctrlH := types.ChordKey{Code: 'h', Mod: types.ChordModCtrl}
	ctrlJ := types.ChordKey{Code: 'j', Mod: types.ChordModCtrl}
	ctrlK := types.ChordKey{Code: 'k', Mod: types.ChordModCtrl}
	ctrlL := types.ChordKey{Code: 'l', Mod: types.ChordModCtrl}
	switch scope {
	case types.SCHEMA_RAIL:
		// The consolidated rail is the single side context: Ctrl+L escapes
		// right into the QueryEditor main pane. Ctrl+K/J vertical rail-stack
		// navigation was dropped with '1'/'2' (there is no stack any more).
		return []*types.ChordBinding{
			{Sequence: []types.ChordKey{ctrlL}, Scope: scope, ActionID: commands.RailSwitchQueryEditor, Description: tr.Actions.RailQueryEditor},
		}
	case types.QUERY_EDITOR:
		return []*types.ChordBinding{
			{Sequence: []types.ChordKey{ctrlH}, Scope: scope, ActionID: commands.RailSwitchLastRail, Description: tr.Actions.RailLastRail},
			{Sequence: []types.ChordKey{ctrlJ}, Scope: scope, ActionID: commands.RailSwitchResults, Description: tr.Actions.RailResults},
		}
	case types.RESULT_GRID, types.PLAN:
		// PLAN tabs occupy the same physical result pane as the grid, so they
		// reuse the grid's directional mapping. Ctrl+H lands on the SCHEMA_RAIL
		// container (RailSwitchLastRail), not a leaf.
		return []*types.ChordBinding{
			{Sequence: []types.ChordKey{ctrlH}, Scope: scope, ActionID: commands.RailSwitchLastRail, Description: tr.Actions.RailLastRail},
			{Sequence: []types.ChordKey{ctrlK}, Scope: scope, ActionID: commands.RailSwitchQueryEditor, Description: tr.Actions.RailQueryEditor},
		}
	}
	return nil
}

// ResultsContextResolver returns the IBaseContext of the active result
// tab, or nil when no tab is open. The orchestrator wires this through
// to the live ResultTabsHelper at boot; tests pass a nil resolver and
// the digit-6 / cycle-to-results path falls through to a no-op.
type ResultsContextResolver func() types.IBaseContext

// RegisterRailSwitchActions registers the rail-switch action IDs with reg,
// each wired to Push() the named context onto the focus stack. tree owns the
// focus stack; ctxTree holds the static Context instances; resolveResults
// resolves the dynamic result-tab context.
// digit 3 jumps to the QueryEditor main pane; digit 4 jumps to the active
// result tab; Ctrl+H (RailSwitchLastRail) returns focus to the consolidated
// SCHEMA_RAIL container; Tab cycles schema_rail→query_editor→results
// →schema_rail. The '1'/'2' leaf jumps and the Ctrl+K/J vertical rail-stack
// navigation were removed (the SCHEMA_RAIL container is the single side
// context and owns its own '['/']' tab cycle via SchemaRailController).
//
// Push (not Replace) is used because the QueryEditor / result tabs are
// MAIN_CONTEXT while the rails are SIDE_CONTEXT — ContextTree.Push has
// the right per-kind semantics: SIDE_CONTEXT wipes the stack (clean
// rail state), MAIN_CONTEXT removes any existing MAIN and appends
// (layers on top of the rail at the bottom). Replace would leak
// QueryEditor onto a rail slot or stack two SIDE_CONTEXTs.
//
// Idempotent: ErrDuplicateAction from a re-registration is swallowed.
// nil reg/tree/ctxTree falls back to a no-op registration so tests that
// build a partial wiring continue to compile. nil resolveResults is
// equivalent to "no result tabs ever exist" — digit 6 and cycle-to-
// results are silent no-ops, mirroring the pre-usj behaviour.
func RegisterRailSwitchActions(reg *commands.Registry, tree *gui.ContextTree, ctxTree *context.ContextTree, resolveResults ResultsContextResolver) {
	if reg == nil {
		return
	}
	if tree == nil || ctxTree == nil {
		noop := func(commands.ExecCtx) error { return nil }
		for _, id := range []string{
			commands.RailSwitchQueryEditor,
			commands.RailSwitchResults,
			commands.RailSwitchNext,
			commands.RailSwitchLastRail,
		} {
			_ = reg.Register(&commands.Command{ID: id, Description: id, Handler: noop})
		}
		return
	}

	// The consolidated SCHEMA_RAIL container is the single side context.
	// Ctrl+H from the editor / result pane (RailSwitchLastRail) and <tab>
	// cycle both push it (NEVER a leaf — the leaves are never on the focus
	// stack). railContainer is nil-safe: a nil push is a no-op.
	railContainer := ctxTree.SchemaRail
	pushRail := func(target types.IBaseContext) error {
		if target == nil {
			return nil
		}
		return tree.Push(target)
	}

	_ = reg.Register(&commands.Command{
		ID:          commands.RailSwitchQueryEditor,
		Description: commands.RailSwitchQueryEditor,
		Handler:     func(commands.ExecCtx) error { return pushRail(ctxTree.QueryRail) },
	})

	_ = reg.Register(&commands.Command{
		ID:          commands.RailSwitchLastRail,
		Description: commands.RailSwitchLastRail,
		Handler:     func(commands.ExecCtx) error { return pushRail(railContainer) },
	})

	_ = reg.Register(&commands.Command{
		ID:          commands.RailSwitchResults,
		Description: commands.RailSwitchResults,
		Handler: func(commands.ExecCtx) error {
			if resolveResults != nil {
				if target := resolveResults(); target != nil {
					return tree.Push(target)
				}
			}
			return tree.Push(ctxTree.QueryRail)
		},
	})

	// Tab cycles linearly through the consolidated SCHEMA_RAIL container,
	// the QueryEditor main pane, and the active result tab. The result entry
	// is a closure that resolves dynamically — if no tab is open, cycle skips
	// that slot. Lookup the next entry from the current view name; if the
	// current view is not in the cycle (e.g. focus is on a popup that somehow
	// leaked Tab through), fall through to the rail container as a safe
	// default.
	type cycleEntry struct {
		// resolve returns the next IBaseContext to push, or nil when the
		// entry is currently unavailable (e.g. results when no tab open).
		resolve func() types.IBaseContext
		// viewName returns the view-name identifying this entry on the
		// focus stack. The result entry uses the live active tab's view
		// name so the current-view lookup matches result_tab_<slot>.
		viewName func() string
	}
	staticEntry := func(c types.IBaseContext) cycleEntry {
		return cycleEntry{
			resolve: func() types.IBaseContext { return c },
			viewName: func() string {
				if c == nil {
					return ""
				}
				return c.GetViewName()
			},
		}
	}
	cycle := []cycleEntry{
		staticEntry(railContainer),
		staticEntry(ctxTree.QueryRail),
		{
			resolve: func() types.IBaseContext {
				if resolveResults == nil {
					return nil
				}
				return resolveResults()
			},
			viewName: func() string {
				if resolveResults == nil {
					return ""
				}
				c := resolveResults()
				if c == nil {
					return ""
				}
				return c.GetViewName()
			},
		},
	}
	_ = reg.Register(&commands.Command{
		ID:          commands.RailSwitchNext,
		Description: commands.RailSwitchNext,
		Handler: func(commands.ExecCtx) error {
			cur := tree.Current()
			if cur == nil {
				return pushRail(railContainer)
			}
			curName := cur.GetViewName()
			for i := range cycle {
				if cycle[i].viewName() != curName {
					continue
				}
				// Walk forward looking for the next entry whose resolve
				// returns non-nil; skips an absent result slot rather
				// than wrapping the cycle through nil.
				for off := 1; off <= len(cycle); off++ {
					next := cycle[(i+off)%len(cycle)].resolve()
					if next != nil {
						return pushRail(next)
					}
				}
				return nil
			}
			return pushRail(railContainer)
		},
	})
}

// queryRailTabCount is the number of tabs in the QUERY_RAIL container
// (QueryEditor, SavedQuery, History) — the modulus the cycle handlers wrap
// over. Mirrors schemaRailTabCount.
const queryRailTabCount = 3

// QUERY_RAIL tab indices. The order is declared in pkg/gui/context/setup.go
// where the container is built (QueryEditor / SavedQuery / History). The
// leaf controllers' <cr>/<esc> switch back to QueryRailEditorTab; <leader>h /
// <leader>o switch to the History / Saved tabs respectively.
const (
	QueryRailEditorTab  = 0
	QueryRailSavedTab   = 1
	QueryRailHistoryTab = 2
)

// QueryRailTabber is the minimal seam RegisterQueryRailTabActions needs from
// the QUERY_RAIL container: read the active tab and set it. *QueryRailContext
// satisfies it. Exported so the orchestrator can build a genuinely-nil
// interface value at the callsite (avoiding the typed-nil trap) and so tests
// can inject a fake without the concrete container.
type QueryRailTabber interface {
	ActiveTab() int
	SetActiveTab(int)
}

// RegisterQueryRailTabActions registers the QUERY_RAIL tab-cycle handlers
// (QueryRailTabNext / QueryRailTabPrev) with reg. The handlers compute the
// edge-wrapped index over queryRailTabCount then call SetActiveTab. The
// container is captured by reference; a nil container makes both handlers
// no-ops. Mirrors RegisterRailSwitchActions' decoupled-registration pattern —
// the per-leaf bindings just publish the ActionID strings.
//
// Idempotent: ErrDuplicateAction from a re-registration is swallowed.
func RegisterQueryRailTabActions(reg *commands.Registry, rail QueryRailTabber) {
	if reg == nil {
		return
	}
	next := func(commands.ExecCtx) error {
		if rail == nil {
			return nil
		}
		rail.SetActiveTab((rail.ActiveTab() + 1) % queryRailTabCount)
		return nil
	}
	prev := func(commands.ExecCtx) error {
		if rail == nil {
			return nil
		}
		rail.SetActiveTab((rail.ActiveTab() - 1 + queryRailTabCount) % queryRailTabCount)
		return nil
	}
	_ = reg.Register(&commands.Command{ID: commands.QueryRailTabNext, Description: "Next query-rail tab", Handler: next})
	_ = reg.Register(&commands.Command{ID: commands.QueryRailTabPrev, Description: "Previous query-rail tab", Handler: prev})
}
