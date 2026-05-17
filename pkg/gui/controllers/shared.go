package controllers

import (
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
// INDEXES from any of them. The Handler closures are no-ops at the
// controller layer: the bootstrap (T10) wires the real ContextTree.Push
// in via the global controller; T7a only declares the inventory so the
// menu and the test-recorder see them.
//
// Per the AC list ("Each side-rail controller registers j/k/digit/<tab>")
// the controller MUST publish these bindings so the bindings menu (?)
// can list them. The actual switch action is wired downstream.
func railSwitchBindings(view string, tr *i18n.TranslationSet) []*types.ChordBinding {
	noop := func() error { return nil }
	scope := types.ContextKey(view)
	return []*types.ChordBinding{
		{
			ViewName:    view,
			Sequence:    []types.ChordKey{{Code: '1'}},
			Scope:       scope,
			Handler:     noop,
			Description: tr.Actions.RailSchemas,
		},
		{
			ViewName:    view,
			Sequence:    []types.ChordKey{{Code: '2'}},
			Scope:       scope,
			Handler:     noop,
			Description: tr.Actions.RailTables,
		},
		{
			ViewName:    view,
			Sequence:    []types.ChordKey{{Code: '3'}},
			Scope:       scope,
			Handler:     noop,
			Description: tr.Actions.RailColumns,
		},
		{
			ViewName:    view,
			Sequence:    []types.ChordKey{{Code: '4'}},
			Scope:       scope,
			Handler:     noop,
			Description: tr.Actions.RailIndexes,
		},
		{
			ViewName:    view,
			Sequence:    []types.ChordKey{{Special: types.KeyTab}},
			Scope:       scope,
			Handler:     noop,
			Description: tr.Actions.RailSchemas,
		},
	}
}
