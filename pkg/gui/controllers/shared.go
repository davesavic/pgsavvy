package controllers

import (
	"github.com/davesavic/dbsavvy/pkg/gui"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/i18n"
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
// between SCHEMAS/TABLES/COLUMNS/INDEXES/QUERY_EDITOR from any of them.
// The handlers are registered ONCE per process via
// RegisterRailSwitchActions; the per-controller bindings just publish
// ActionID strings the Matcher resolves through the commands.Registry.
func railSwitchBindings(view string, tr *i18n.TranslationSet) []*types.ChordBinding {
	scope := types.ContextKey(view)
	return []*types.ChordBinding{
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
			ActionID:    commands.RailSwitchColumns,
			Description: tr.Actions.RailColumns,
		},
		{
			Sequence:    []types.ChordKey{{Code: '4'}},
			Scope:       scope,
			ActionID:    commands.RailSwitchIndexes,
			Description: tr.Actions.RailIndexes,
		},
		{
			Sequence:    []types.ChordKey{{Code: '5'}},
			Scope:       scope,
			ActionID:    commands.RailSwitchQueryEditor,
			Description: tr.Actions.RailQueryEditor,
		},
		{
			Sequence:    []types.ChordKey{{Code: '6'}},
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
			commands.RailSwitchColumns,
			commands.RailSwitchIndexes,
			commands.RailSwitchQueryEditor,
			commands.RailSwitchResults,
			commands.RailSwitchNext,
		} {
			_ = reg.Register(&commands.Command{ID: id, Description: id, Handler: noop})
		}
		return
	}

	jumpTo := func(target types.IBaseContext) func(commands.ExecCtx) error {
		return func(commands.ExecCtx) error {
			if target == nil {
				return nil
			}
			return tree.Push(target)
		}
	}

	_ = reg.Register(&commands.Command{ID: commands.RailSwitchSchemas, Description: commands.RailSwitchSchemas, Handler: jumpTo(ctxTree.Schemas)})
	_ = reg.Register(&commands.Command{ID: commands.RailSwitchTables, Description: commands.RailSwitchTables, Handler: jumpTo(ctxTree.Tables)})
	_ = reg.Register(&commands.Command{ID: commands.RailSwitchColumns, Description: commands.RailSwitchColumns, Handler: jumpTo(ctxTree.Columns)})
	_ = reg.Register(&commands.Command{ID: commands.RailSwitchIndexes, Description: commands.RailSwitchIndexes, Handler: jumpTo(ctxTree.Indexes)})
	_ = reg.Register(&commands.Command{ID: commands.RailSwitchQueryEditor, Description: commands.RailSwitchQueryEditor, Handler: jumpTo(ctxTree.QueryEditor)})

	// Digit 6 — push the active result tab onto the focus stack. The
	// resolver is invoked at fire time so the dispatch always sees the
	// current active tab. nil resolver / nil active tab silently no-ops
	// (no toast — keystrokes that produce nothing are common in TUIs and
	// the user gets immediate visual feedback via the focus border).
	_ = reg.Register(&commands.Command{
		ID:          commands.RailSwitchResults,
		Description: commands.RailSwitchResults,
		Handler: func(commands.ExecCtx) error {
			if resolveResults == nil {
				return nil
			}
			target := resolveResults()
			if target == nil {
				return nil
			}
			return tree.Push(target)
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
		staticEntry(ctxTree.Connections),
		staticEntry(ctxTree.Schemas),
		staticEntry(ctxTree.Tables),
		staticEntry(ctxTree.Columns),
		staticEntry(ctxTree.Indexes),
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
				return tree.Push(ctxTree.Schemas)
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
						return tree.Push(next)
					}
				}
				return nil
			}
			return tree.Push(ctxTree.Schemas)
		},
	})
}
