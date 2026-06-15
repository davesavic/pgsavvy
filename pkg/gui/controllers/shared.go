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

// railSwitchBindings returns the digit-1..5 + <tab> bindings every side
// rail (and the QUERY_EDITOR main pane) registers so the user can hop
// between SCHEMAS/TABLES/QUERY_EDITOR/RESULTS from any of them.
// The handlers are registered ONCE per process via
// RegisterRailSwitchActions; the per-controller bindings just publish
// ActionID strings the Matcher resolves through the commands.Registry.
//
// It also appends per-view directional Ctrl+H/J/K/L chords.
// The right-hand main column is split vertically (boxlayout.ROW puts
// children top-to-bottom), so QueryEditor sits ABOVE Results — the
// j/k axis covers that edge, not h/l:
//
//   - on the five side rails: Ctrl+K = prev rail, Ctrl+J = next rail,
//     Ctrl+L = QueryEditor. The Up/Down handlers no-op at the ends.
//   - on QUERY_EDITOR: Ctrl+H = last-focused rail, Ctrl+J = active
//     result tab. Mode defaults to ModeNormal so Insert mode keeps
//     Ctrl+H = Backspace and Ctrl+J = LineFeed via gocui DefaultEditor.
//   - on RESULT_GRID: Ctrl+H = last-focused rail (rails are left of
//     the whole main column, not just the editor), Ctrl+K = QueryEditor.
func railSwitchBindings(view string, tr *i18n.TranslationSet) []*types.ChordBinding {
	scope := types.ContextKey(view)
	out := []*types.ChordBinding{
		{
			Sequence:    []types.ChordKey{{Code: '1'}},
			Scope:       scope,
			ActionID:    commands.RailSwitchSchemas,
			Description: tr.Actions.RailSchemas,
		},
		{
			Sequence:    []types.ChordKey{{Code: '2'}},
			Scope:       scope,
			ActionID:    commands.RailSwitchTables,
			Description: tr.Actions.RailTables,
		},
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
			Description: tr.Actions.RailSchemas,
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
	case types.SCHEMAS:
		return []*types.ChordBinding{
			{Sequence: []types.ChordKey{ctrlK}, Scope: scope, ActionID: commands.RailSwitchUp, Description: tr.Actions.RailUp},
			{Sequence: []types.ChordKey{ctrlJ}, Scope: scope, ActionID: commands.RailSwitchDown, Description: tr.Actions.RailDown},
			{Sequence: []types.ChordKey{ctrlL}, Scope: scope, ActionID: commands.RailSwitchQueryEditor, Description: tr.Actions.RailQueryEditor},
		}
	case types.TABLES:
		return []*types.ChordBinding{
			{Sequence: []types.ChordKey{ctrlK}, Scope: scope, ActionID: commands.RailSwitchUp, Description: tr.Actions.RailUp},
			{Sequence: []types.ChordKey{ctrlJ}, Scope: scope, ActionID: commands.RailSwitchDown, Description: tr.Actions.RailDown},
			{Sequence: []types.ChordKey{ctrlL}, Scope: scope, ActionID: commands.RailSwitchResults, Description: tr.Actions.RailResults},
		}
	case types.QUERY_EDITOR:
		return []*types.ChordBinding{
			{Sequence: []types.ChordKey{ctrlH}, Scope: scope, ActionID: commands.RailSwitchLastRail, Description: tr.Actions.RailLastRail},
			{Sequence: []types.ChordKey{ctrlJ}, Scope: scope, ActionID: commands.RailSwitchResults, Description: tr.Actions.RailResults},
		}
	case types.RESULT_GRID, types.PLAN:
		// PLAN tabs occupy the same physical result pane as the grid, so they
		// reuse the grid's directional mapping.
		return []*types.ChordBinding{
			{Sequence: []types.ChordKey{ctrlH}, Scope: scope, ActionID: commands.RailSwitchTables, Description: tr.Actions.RailTables},
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

// RegisterRailSwitchActions registers the seven rail-switch action IDs
// with reg, each wired to Push() the named context onto the focus
// stack. tree owns the focus stack; ctxTree holds the static Context
// instances; resolveResults resolves the dynamic result-tab context.
// 1/2/3/4 jump to Schemas/Tables/Columns/Indexes; 5 jumps to the
// QueryEditor main pane; 6 jumps to the active result tab; Tab cycles
// connections→schemas→tables→columns→indexes→query_editor→results
// →connections.
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
			commands.RailSwitchSchemas,
			commands.RailSwitchTables,
			commands.RailSwitchQueryEditor,
			commands.RailSwitchResults,
			commands.RailSwitchNext,
			commands.RailSwitchUp,
			commands.RailSwitchDown,
			commands.RailSwitchLastRail,
		} {
			_ = reg.Register(&commands.Command{ID: id, Description: id, Handler: noop})
		}
		return
	}

	// last-focused rail tracking for the editor's Ctrl+H
	// round-trip. Updated by pushRail whenever the push lands on one
	// of the three SIDE_CONTEXT rails; consumed by RailSwitchLastRail.
	// Defaults to SCHEMAS so the very first Ctrl+H from the editor
	// (before any rail has been focused this session) lands somewhere
	// sensible. Rail-switch handlers all run on the gocui dispatch
	// goroutine, so this needs no mutex.
	railOrder := []types.IBaseContext{
		ctxTree.Schemas,
		ctxTree.Tables,
	}
	lastRailKey := types.SCHEMAS
	railKeys := map[types.ContextKey]struct{}{
		types.SCHEMAS: {},
		types.TABLES:  {},
	}
	pushRail := func(target types.IBaseContext) error {
		if target == nil {
			return nil
		}
		if err := tree.Push(target); err != nil {
			return err
		}
		if _, ok := railKeys[target.GetKey()]; ok {
			lastRailKey = target.GetKey()
		}
		return nil
	}

	jumpTo := func(target types.IBaseContext) func(commands.ExecCtx) error {
		return func(commands.ExecCtx) error {
			return pushRail(target)
		}
	}

	_ = reg.Register(&commands.Command{ID: commands.RailSwitchSchemas, Description: commands.RailSwitchSchemas, Handler: jumpTo(ctxTree.Schemas)})
	_ = reg.Register(&commands.Command{ID: commands.RailSwitchTables, Description: commands.RailSwitchTables, Handler: jumpTo(ctxTree.Tables)})
	_ = reg.Register(&commands.Command{ID: commands.RailSwitchQueryEditor, Description: commands.RailSwitchQueryEditor, Handler: jumpTo(ctxTree.QueryEditor)})

	// directional rail navigation handlers (Ctrl+K/J/H on
	// QUERY_EDITOR / side rails). Up/Down walk the vertical rail stack
	// based on the current focus; no-op at the ends and when the
	// current focus is not a rail. LastRail consults the lastRailKey
	// tracker pushRail maintains.
	indexOfCurrentRail := func() int {
		cur := tree.Current()
		if cur == nil {
			return -1
		}
		name := cur.GetViewName()
		for i, r := range railOrder {
			if r != nil && r.GetViewName() == name {
				return i
			}
		}
		return -1
	}
	_ = reg.Register(&commands.Command{
		ID:          commands.RailSwitchUp,
		Description: commands.RailSwitchUp,
		Handler: func(commands.ExecCtx) error {
			i := indexOfCurrentRail()
			if i <= 0 {
				return nil
			}
			return pushRail(railOrder[i-1])
		},
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.RailSwitchDown,
		Description: commands.RailSwitchDown,
		Handler: func(commands.ExecCtx) error {
			i := indexOfCurrentRail()
			if i < 0 || i >= len(railOrder)-1 {
				return nil
			}
			return pushRail(railOrder[i+1])
		},
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.RailSwitchLastRail,
		Description: commands.RailSwitchLastRail,
		Handler: func(commands.ExecCtx) error {
			for _, r := range railOrder {
				if r != nil && r.GetKey() == lastRailKey {
					return pushRail(r)
				}
			}
			return pushRail(ctxTree.Schemas)
		},
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
			return tree.Push(ctxTree.QueryEditor)
		},
	})

	// Tab cycles linearly through every rail plus the QueryEditor main
	// pane and the active result tab. The result entry is a closure that
	// resolves dynamically — if no tab is open, cycle skips that slot.
	// Lookup the next entry from the current view name; if the current
	// view is not in the cycle (e.g. focus is on a popup that somehow
	// leaked Tab through), fall through to Schemas as a safe default.
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
			resolve:  func() types.IBaseContext { return c },
			viewName: func() string { return c.GetViewName() },
		}
	}
	cycle := []cycleEntry{
		staticEntry(ctxTree.Schemas),
		staticEntry(ctxTree.Tables),
		staticEntry(ctxTree.QueryEditor),
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
				return pushRail(ctxTree.Schemas)
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
			return pushRail(ctxTree.Schemas)
		},
	})
}
