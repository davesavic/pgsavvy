package controllers

import (
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// AllDefaultBindings returns the union of every controller's
// GetKeybindings output plus the COMMAND_LINE default bindings.
//
// The order is: Quit, Connections, Schemas, Tables, Columns, Indexes,
// Menu, then keys.DefaultCommandLineBindings. Nil controllers are
// skipped. This is the shipped-default slice the orchestrator hands to
// keys.KeybindingService.Build during wireWithDriver and re-uses on
// every :reload.
func AllDefaultBindings(c *Controllers) []*types.ChordBinding {
	var out []*types.ChordBinding
	if c == nil {
		return append(out, keys.DefaultCommandLineBindings()...)
	}
	if c.Quit != nil {
		out = append(out, c.Quit.GetKeybindings(types.KeybindingsOpts{})...)
	}
	if c.Connections != nil {
		out = append(out, c.Connections.GetKeybindings(types.KeybindingsOpts{})...)
	}
	if c.Schemas != nil {
		out = append(out, c.Schemas.GetKeybindings(types.KeybindingsOpts{})...)
	}
	if c.Tables != nil {
		out = append(out, c.Tables.GetKeybindings(types.KeybindingsOpts{})...)
	}
	if c.Columns != nil {
		out = append(out, c.Columns.GetKeybindings(types.KeybindingsOpts{})...)
	}
	if c.Indexes != nil {
		out = append(out, c.Indexes.GetKeybindings(types.KeybindingsOpts{})...)
	}
	if c.Menu != nil {
		out = append(out, c.Menu.GetKeybindings(types.KeybindingsOpts{})...)
	}
	if c.Prompt != nil {
		out = append(out, c.Prompt.GetKeybindings(types.KeybindingsOpts{})...)
	}
	if c.Selection != nil {
		out = append(out, c.Selection.GetKeybindings(types.KeybindingsOpts{})...)
	}
	if c.Confirmation != nil {
		out = append(out, c.Confirmation.GetKeybindings(types.KeybindingsOpts{})...)
	}
	if c.QueryEditor != nil {
		out = append(out, c.QueryEditor.GetKeybindings(types.KeybindingsOpts{})...)
	}
	if c.ResultTabs != nil {
		out = append(out, c.ResultTabs.GetKeybindings(types.KeybindingsOpts{})...)
	}
	if c.VimEditor != nil {
		out = append(out, c.VimEditor.GetKeybindings(types.KeybindingsOpts{})...)
	}
	if c.Plan != nil {
		out = append(out, c.Plan.GetKeybindings(types.KeybindingsOpts{})...)
	}
	if c.HideOverlay != nil {
		out = append(out, c.HideOverlay.GetKeybindings(types.KeybindingsOpts{})...)
	}
	if c.ExportMenu != nil {
		out = append(out, c.ExportMenu.GetKeybindings(types.KeybindingsOpts{})...)
	}
	out = append(out, keys.DefaultCommandLineBindings()...)
	return out
}
