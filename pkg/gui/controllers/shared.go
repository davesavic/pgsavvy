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
			Sequence:    []types.ChordKey{{Special: types.KeyTab}},
			Scope:       scope,
			ActionID:    commands.RailSwitchNext,
			Description: tr.Actions.RailSchemas,
		},
	}
}

// RegisterRailSwitchActions registers the six rail-switch action IDs
// with reg, each wired to Push() the named context onto the focus
// stack. tree owns the focus stack; ctxTree holds the Context
// instances. 1/2/3/4 jump to Schemas/Tables/Columns/Indexes; 5 jumps
// to the QueryEditor main pane; Tab cycles
// connections→schemas→tables→columns→indexes→query_editor→connections.
//
// Push (not Replace) is used because the QueryEditor is MAIN_CONTEXT
// while the rails are SIDE_CONTEXT — ContextTree.Push has the right
// per-kind semantics: SIDE_CONTEXT wipes the stack (clean rail state),
// MAIN_CONTEXT removes any existing MAIN and appends (layers on top of
// the rail at the bottom). Replace would leak QueryEditor onto a rail
// slot or stack two SIDE_CONTEXTs.
//
// Idempotent: ErrDuplicateAction from a re-registration is swallowed.
// nil reg/tree/ctxTree falls back to a no-op registration so tests that
// build a partial wiring continue to compile.
func RegisterRailSwitchActions(reg *commands.Registry, tree *gui.ContextTree, ctxTree *context.ContextTree) {
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

	// Tab cycles linearly through every rail plus the QueryEditor main
	// pane. Lookup the next entry from the current view name; if the
	// current view is not in the cycle (e.g. focus is on a popup that
	// somehow leaked Tab through), fall through to Schemas as a safe
	// default.
	cycle := []types.IBaseContext{
		ctxTree.Connections,
		ctxTree.Schemas,
		ctxTree.Tables,
		ctxTree.Columns,
		ctxTree.Indexes,
		ctxTree.QueryEditor,
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
			for i, c := range cycle {
				if c == nil {
					continue
				}
				if c.GetViewName() == curName {
					next := cycle[(i+1)%len(cycle)]
					if next == nil {
						return nil
					}
					return tree.Push(next)
				}
			}
			return tree.Push(ctxTree.Schemas)
		},
	})
}
