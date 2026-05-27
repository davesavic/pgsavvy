package controllers

import (
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// AllDefaultBindings returns the union of every controller's
// GetKeybindings output plus the COMMAND_LINE default bindings.
//
// It is DERIVED: it iterates the single per-controller registry
// (Controllers.entries(), one entry per non-nil controller field) and
// concatenates each entry's GetKeybindings, then appends
// keys.DefaultCommandLineBindings. A nil bundle yields just the
// command-line defaults. This is the shipped-default slice the
// orchestrator hands to keys.KeybindingService.Build during wireWithDriver
// and re-uses on every :reload.
//
// Ordering follows entries()'s declaration order; the binding-snapshot
// oracle sorts the tuples, so the union — not the order — is what the
// snapshot pins.
func AllDefaultBindings(c *Controllers) []*types.ChordBinding {
	var out []*types.ChordBinding
	for _, e := range c.entries() {
		out = append(out, e.ctrl.GetKeybindings(types.KeybindingsOpts{})...)
	}
	out = append(out, keys.DefaultCommandLineBindings()...)
	return out
}
