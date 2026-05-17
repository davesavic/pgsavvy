package commands

// Action IDs.
//
// These string constants are the stable contract between:
//
//   - pkg/gui/controllers           (publish bindings citing these IDs)
//   - pkg/config                    (validates `action: …` strings in user config)
//   - pkg/gui/keys                  (resolves IDs to Handlers via the Registry)
//   - pkg/cheatsheet                (renders by ID)
//
// IDs are dot-namespaced (`namespace.verb` or `namespace.subnamespace.verb`).
// A new ID is added here BEFORE any controller registers it.
//
// Sources:
//   - DESIGN.md §10.10 config schema example
//   - Existing E1–E4 controller publishings (pre-dlp.8 KeyBinding lists)
//
// Out of scope for dlp.1: query/result/cursor/pane families (added by
// later epics dbsavvy-66p, dbsavvy-wwd, etc.). dlp.7 adds `:reload`
// via the ExRegistry, not the CommandRegistry — so no `reload.config`
// constant appears here.
const (
	// AppQuit — owned by QuitController. Maps to `<leader>q` by default.
	AppQuit = "app.quit"

	// SchemaHide / SchemaUnhide / SchemaToggleShowHidden — owned by
	// SchemasController. Mapped to `H`, `U`, `<leader>H` respectively.
	SchemaHide             = "schema.hide"
	SchemaUnhide           = "schema.unhide"
	SchemaToggleShowHidden = "schema.toggle_show_hidden"

	// ConnectionAdd — owned by ConnectionsController. Maps to `a`.
	ConnectionAdd = "connection.add"

	// ListUp / ListDown / ListConfirm — published by ListControllerTrait
	// (shared by every side-rail). `j`/`k`/`<cr>`.
	ListUp      = "list.up"
	ListDown    = "list.down"
	ListConfirm = "list.confirm"

	// Rail-switch family — published by every side-rail controller via
	// railSwitchBindings (pkg/gui/controllers/shared.go). Digits 1..4
	// jump to a specific rail; `<tab>` cycles to the next rail.
	RailSwitchSchemas = "rail.switch.schemas"
	RailSwitchTables  = "rail.switch.tables"
	RailSwitchColumns = "rail.switch.columns"
	RailSwitchIndexes = "rail.switch.indexes"
	RailSwitchNext    = "rail.switch.next"

	// MenuConfirm / MenuCancel — owned by MenuController. `<cr>` / `<esc>`
	// inside the MENU popup context.
	MenuConfirm = "menu.confirm"
	MenuCancel  = "menu.cancel"

	// CommandOpen — owned globally; opens the COMMAND_LINE context. `:`
	// CommandCancel — owned by COMMAND_LINE context; closes it. `<esc>`
	// (CommandCancel is consumed by dlp.7's COMMAND_LINE bindings.)
	CommandOpen   = "command.open"
	CommandCancel = "command.cancel"

	// HelpCheatsheet — opens the auto-generated cheatsheet popup. `?`
	HelpCheatsheet = "help.cheatsheet"
)

// AllActionIDs returns every ID declared in this file in declaration
// order. Useful for tests that want to assert every constant is
// non-empty, dot-namespaced, and unique without enumerating them by
// name. New constants MUST be appended here so the test catches the
// addition.
func AllActionIDs() []string {
	return []string{
		AppQuit,
		SchemaHide,
		SchemaUnhide,
		SchemaToggleShowHidden,
		ConnectionAdd,
		ListUp,
		ListDown,
		ListConfirm,
		RailSwitchSchemas,
		RailSwitchTables,
		RailSwitchColumns,
		RailSwitchIndexes,
		RailSwitchNext,
		MenuConfirm,
		MenuCancel,
		CommandOpen,
		CommandCancel,
		HelpCheatsheet,
	}
}
