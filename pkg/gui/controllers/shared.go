package controllers

import (
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
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

// railSwitchBindings returns the digit-1..4 + <tab> bindings every side
// rail registers so the user can hop between SCHEMAS/TABLES/COLUMNS/
// INDEXES from any of them. The handlers are registered ONCE per
// process via registerRailSwitchActions; the per-controller bindings
// just publish ActionID strings the Matcher resolves through the
// commands.Registry.
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
			Sequence:    []types.ChordKey{{Special: types.KeyTab}},
			Scope:       scope,
			ActionID:    commands.RailSwitchNext,
			Description: tr.Actions.RailSchemas,
		},
	}
}

// registerRailSwitchActions registers the five rail-switch action IDs
// with reg using no-op handlers. The real cross-rail focus push is
// owned by the global controller and lands in a downstream epic; today
// we only need every shipped binding's ActionID to resolve to a Command
// during Build so the trie has a leaf to dispatch.
//
// Idempotent: ErrDuplicateAction from a re-registration is swallowed,
// matching the orchestrator's "controllers may register on top of each
// other" contract.
func registerRailSwitchActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	noop := func(commands.ExecCtx) error { return nil }
	for _, id := range []string{
		commands.RailSwitchSchemas,
		commands.RailSwitchTables,
		commands.RailSwitchColumns,
		commands.RailSwitchIndexes,
		commands.RailSwitchNext,
	} {
		_ = reg.Register(&commands.Command{ID: id, Description: id, Handler: noop})
	}
}
