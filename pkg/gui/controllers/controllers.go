package controllers

import (
	"reflect"
	"time"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// Controllers is the bundle of every controller instance the gui owns.
// Returned by AttachControllers; T10 (bootstrap) keeps the bundle so
// individual controllers remain accessible after wiring completes.
type Controllers struct {
	Connections  *ConnectionsController
	Schemas      *SchemasController
	Tables       *TablesController
	Menu         *MenuController
	Prompt       *PromptController
	Selection    *SelectionController
	Confirmation *ConfirmationController
	Quit         *QuitController
	QueryEditor  *QueryEditorController
	Tx           *TxController
	ResultTabs   *ResultTabsController
	HideOverlay  *HideOverlayController
	ExportMenu   *ExportMenuController
	VimEditor    *VimEditorController
	Plan         *PlanController
	// TableInspect is constructed by the orchestrator (it needs a
	// Pop-capable focus-stack handle outside this package). The
	// orchestrator assigns it after AttachControllers returns so the
	// bundle's binding-inventory + RegisterActions paths include it.
	TableInspect *TableInspectController
	// CellEditor / CommitDialog / ConflictDialog / FKReversePicker are
	// constructed by the orchestrator alongside TableInspect: each takes
	// a FocusPopper-capable handle on the focus-stack (*gui.ContextTree)
	// which this package cannot import. The bundle still owns them so
	// RegisterActions + AllDefaultBindings include their bindings.
	CellEditor      *CellEditorController
	CommitDialog    *CommitDialogController
	ConflictDialog  *ConflictDialogController
	FKReversePicker *FKReversePickerController

	// Reconnect owns the <leader>R GLOBAL binding and the reconnect
	// dialog (hq5.7).
	Reconnect *ReconnectController

	// SearchPath owns the <leader>p GLOBAL binding for the search_path
	// quick-set prompt (hq5.10).
	SearchPath *SearchPathController

	// StatementTimeout owns the <leader>tt QUERY_EDITOR binding for the
	// statement timeout prompt (hq5.11).
	StatementTimeout *StatementTimeoutController

	// Cheatsheet is constructed by the orchestrator (it needs a Pop-
	// capable focus-stack handle outside this package). dbsavvy-bwq.Z1
	// promoted the help popup to TabbedPopup; the controller owns the
	// [, ], <tab>, <esc>, q bindings on CHEATSHEET scope.
	Cheatsheet *CheatsheetController
}

// AttachControllers builds every controller, attaches it to its target
// context, and returns the bundle.
//
// HelperBag fields the caller has not yet wired (e.g. T7b's UI helpers)
// can be left nil; every controller nil-checks at call time. The
// pickers in HelperBag fall back to null implementations when the
// caller supplies nil so handlers run as no-ops.
//
// AttachControllers is idempotent only at the controller-instance
// level: calling it twice with the same tree results in two attaches
// per context (and therefore two registrations of every binding).
// The bootstrap (T10) MUST call it exactly once.
func AttachControllers(
	tree *context.ContextTree,
	c *common.Common,
	helpers HelperBag,
) *Controllers {
	if tree == nil {
		return &Controllers{}
	}

	// Default to null pickers when the caller leaves them nil so
	// handlers gracefully no-op.
	if helpers.Connections == nil {
		helpers.Connections = nullConnectionPicker{}
	}
	if helpers.Schemas == nil {
		helpers.Schemas = nullSchemaPicker{}
	}
	if helpers.Tables == nil {
		helpers.Tables = nullTablePicker{}
	}
	if helpers.ActiveConnection == nil {
		helpers.ActiveConnection = nullActiveConnection{}
	}

	connections := NewConnectionsController(c, helpers.CoreDeps, helpers.NavDeps, helpers.UIDeps, helpers.ThreadingDeps, &tree.Connections.SideListContext, helpers.Connections)
	schemas := NewSchemasController(c, helpers.CoreDeps, helpers.NavDeps, helpers.UIDeps, &tree.Schemas.SideListContext, helpers.Schemas)
	tables := NewTablesController(c, helpers.CoreDeps, helpers.NavDeps, &tree.Tables.SideListContext, helpers.Tables)
	menu := NewMenuController(c, helpers.CoreDeps, helpers.UIDeps)
	prompt := NewPromptController(c, helpers.CoreDeps, helpers.UIDeps)
	selection := NewSelectionController(c, helpers.CoreDeps, helpers.UIDeps)
	confirmation := NewConfirmationController(c, helpers.CoreDeps, helpers.UIDeps)
	quit := NewQuitController(c, helpers.CoreDeps, helpers.UIDeps, helpers.QueryDeps, helpers.EditDeps)
	reconnect := NewReconnectController(c, helpers.CoreDeps, helpers.NavDeps, helpers.UIDeps, helpers.QueryDeps, helpers.ThreadingDeps, helpers.EditDeps)
	searchPath := NewSearchPathController(c, helpers.CoreDeps, helpers.NavDeps, helpers.UIDeps, helpers.QueryDeps, helpers.ThreadingDeps)
	stmtTimeout := NewStatementTimeoutController(c, helpers.CoreDeps, helpers.NavDeps, helpers.UIDeps, helpers.QueryDeps, helpers.ThreadingDeps)
	queryEditor := NewQueryEditorController(c, helpers.CoreDeps, helpers.NavDeps, helpers.UIDeps, helpers.QueryDeps)
	tx := NewTxController(c, helpers.CoreDeps, helpers.NavDeps, helpers.UIDeps, helpers.QueryDeps)

	// ResultTabsController publishes RESULT_GRID + GLOBAL bindings; it
	// reaches the trie via AllDefaultBindings. The manager surface is
	// supplied by the orchestrator's HelperBag.ResultTabs (a concrete
	// ui.ResultTabsHelper implementing ResultTabsManager). Tests that
	// don't exercise dispatch leave the manager nil.
	var tabsMgr ResultTabsManager
	if helpers.ResultTabs != nil {
		if m, ok := helpers.ResultTabs.(ResultTabsManager); ok {
			tabsMgr = m
		}
	}
	resultTabs := NewResultTabsController(c, helpers.CoreDeps, helpers.UIDeps, helpers.EditDeps, tabsMgr)

	// HideOverlayController publishes HIDE_OVERLAY-scope bindings for the
	// <leader>gH column-visibility overlay (dbsavvy-uv0.6). The manager
	// surface is the same concrete *ui.ResultTabsHelper that backs
	// ResultTabsManager — typed here through a narrower interface so the
	// controller package stays free of helpers/ui.
	var hideMgr HideOverlayManager
	if helpers.ResultTabs != nil {
		if m, ok := helpers.ResultTabs.(HideOverlayManager); ok {
			hideMgr = m
		}
	}
	hideOverlay := NewHideOverlayController(c, helpers.CoreDeps, hideMgr)

	// ExportMenuController publishes EXPORT_MENU-scope bindings for the
	// <leader>oe export menu (dbsavvy-uv0.9). The manager surface is the
	// same concrete *ui.ResultTabsHelper that backs ResultTabsManager —
	// typed through a narrower interface so the controller package stays
	// free of helpers/ui.
	var exportMgr ExportMenuManager
	if helpers.ResultTabs != nil {
		if m, ok := helpers.ResultTabs.(ExportMenuManager); ok {
			exportMgr = m
		}
	}
	exportMenu := NewExportMenuController(c, helpers.CoreDeps, exportMgr)

	// VimEditorController owns motion / operator / textobject bindings
	// under QUERY_EDITOR scope (epic dbsavvy-wwd). It takes the live
	// *context.QueryEditorContext directly (tree.QueryEditor is the
	// concrete pointer post-wwd.1) plus the keybinding Matcher
	// (wwd.8 uses the Matcher to coordinate operator-pending state).
	// Either dep may be missing in test wiring: skip construction so
	// AttachControllers stays nil-safe.
	var vimEditor *VimEditorController
	if tree.QueryEditor != nil {
		var matcher *keys.Matcher
		if helpers.KbRuntime != nil {
			matcher = helpers.KbRuntime.Matcher
		}
		vimEditor = NewVimEditorController(tree.QueryEditor, matcher)
		// wwd.8 — wire the toast sink for the +/* clipboard one-shot
		// TODO toast. Falls back silently when no Toast helper is
		// injected (unit tests).
		if helpers.Toast != nil {
			toast := helpers.Toast
			vimEditor.SetToaster(func(msg string) {
				toast.Show(msg, 3*time.Second)
			})
		}
	}

	// PlanController publishes PLAN-scoped tree-navigation bindings
	// (dbsavvy-uv0.8). The plan tab's per-tab *context.PlanContext is
	// reached through helpers.ActivePlanContextFn (wired by the
	// orchestrator to ResultTabsHelper.ActivePlanContext).
	plan := NewPlanController(c, helpers.CoreDeps, helpers.ActivePlanContextFn)

	bundle := &Controllers{
		Connections:  connections,
		Schemas:      schemas,
		Tables:       tables,
		Menu:         menu,
		Prompt:       prompt,
		Selection:    selection,
		Confirmation: confirmation,
		Quit:         quit,
		Reconnect:    reconnect,
		SearchPath:       searchPath,
		StatementTimeout: stmtTimeout,
		QueryEditor:      queryEditor,
		Tx:           tx,
		ResultTabs:   resultTabs,
		HideOverlay:  hideOverlay,
		ExportMenu:   exportMenu,
		VimEditor:    vimEditor,
		Plan:         plan,
	}

	// Single attach pass driven by the per-controller registry. attachTargets
	// maps each registry entry name to the context whose AddKeybindingsFn the
	// controller subscribes to; entries with attach==false (VimEditor) — and
	// any entry without a target here (the 6 orchestrator-constructed
	// controllers, still nil at this point) — are skipped. tree.QueryEditor /
	// tree.ResultGrid / tree.Plan are reached via their IBaseContext handle
	// (AddKeybindingsFn is a no-op on the stub contexts today; the wiring
	// lights up automatically once the live contexts ship).
	attachTargets := map[string]attachable{
		"Connections":  &tree.Connections.BaseContext,
		"Schemas":      &tree.Schemas.BaseContext,
		"Tables":       &tree.Tables.BaseContext,
		"Menu":         &tree.Menu.BaseContext,
		"Prompt":       &tree.Prompt.BaseContext,
		"Selection":    &tree.Selection.BaseContext,
		"Confirmation": &tree.Confirmation.BaseContext,
		"Quit":         &tree.Global.BaseContext,
		"Reconnect":    &tree.Global.BaseContext,
		"SearchPath":       &tree.Global.BaseContext,
		"StatementTimeout": tree.QueryEditor,
		"QueryEditor":      tree.QueryEditor,
		"Tx":           tree.QueryEditor,
		"ResultTabs":   tree.ResultGrid,
		"HideOverlay":  &tree.HideOverlay.BaseContext,
		"ExportMenu":   &tree.ExportMenu.BaseContext,
		"Plan":         tree.Plan,
	}
	for _, e := range bundle.entries() {
		if !e.attach {
			continue
		}
		target, ok := attachTargets[e.name]
		if !ok {
			continue
		}
		// Every attach==true controller implements AttachToContext; the
		// assertion always holds. VimEditor (the lone non-implementer) is
		// already filtered out by attach==false above.
		if a, ok := e.ctrl.(attachableController); ok {
			a.AttachToContext(target)
		}
	}

	return bundle
}

// attachableController is the attach-pass method set: a registry-listed
// controller that also subscribes to a context. Every controller except
// VimEditor satisfies it.
type attachableController interface {
	controllerRegistrant
	AttachToContext(ctx attachable)
}

// controllerEntry is one row of the per-controller registry returned by
// Controllers.entries(): a non-nil controller field, its struct-field name,
// and whether AttachControllers subscribes it to a context via
// AttachToContext. The registry is the single source the three derived paths
// iterate — AttachControllers (attach), AllDefaultBindings (GetKeybindings
// union), and RegisterActions (action registration) — so a new controller
// field is picked up by all three at once.
type controllerEntry struct {
	// name is the Controllers struct-field name. Used by AttachControllers
	// to resolve the attach target and asserted by the completeness guard.
	name string
	// ctrl is the controller. controllerRegistrant is the method set the
	// always-derived paths need (GetKeybindings + RegisterActions); attach
	// entries additionally satisfy attachableController (asserted in the
	// attach pass).
	ctrl controllerRegistrant
	// attach reports whether AttachControllers should call AttachToContext.
	// VimEditor is the ONLY attach==false entry: its bindings reach the trie
	// via AllDefaultBindings and the Matcher routes keystrokes to the
	// QUERY_EDITOR scope, so no per-context AddKeybindingsFn call is needed.
	attach bool
}

// controllerRegistrant is the method set EVERY controller satisfies and that
// the always-derived paths consume: GetKeybindings (AllDefaultBindings union)
// and RegisterActions (action registration). It intentionally does NOT
// require AttachToContext — VimEditor is the one controller that lacks it, so
// the attach pass type-asserts to attachable instead and VimEditor's
// attach==false entry is skipped before any assertion.
type controllerRegistrant interface {
	GetKeybindings(types.KeybindingsOpts) []*types.ChordBinding
	RegisterActions(reg *commands.Registry)
}

// entries returns one controllerEntry per NON-NIL controller field in the
// bundle, in declaration order. It is the single registry the three derived
// paths iterate. The 6 orchestrator-constructed controllers (TableInspect,
// CellEditor, CommitDialog, ConflictDialog, FKReversePicker, Cheatsheet) are
// nil until the orchestrator assigns them; they are skipped here when nil and
// picked up automatically once non-nil (AllDefaultBindings / RegisterActions
// run after full construction at gui.go:1289). VimEditor is the only
// attach==false entry.
//
// Every controller satisfies controllerRegistrant (GetKeybindings +
// RegisterActions). All except VimEditor additionally satisfy
// attachableController; VimEditor's entry is attach=false so the attach pass
// never asserts it. Adding a controller field to Controllers without adding
// it here is caught by TestAllDefaultBindingsIncludesEveryProviderController.
func (b *Controllers) entries() []controllerEntry {
	if b == nil {
		return nil
	}
	candidates := []controllerEntry{
		{name: "Connections", ctrl: b.Connections, attach: true},
		{name: "Schemas", ctrl: b.Schemas, attach: true},
		{name: "Tables", ctrl: b.Tables, attach: true},
		{name: "Menu", ctrl: b.Menu, attach: true},
		{name: "Prompt", ctrl: b.Prompt, attach: true},
		{name: "Selection", ctrl: b.Selection, attach: true},
		{name: "Confirmation", ctrl: b.Confirmation, attach: true},
		{name: "Quit", ctrl: b.Quit, attach: true},
		{name: "Reconnect", ctrl: b.Reconnect, attach: true},
		{name: "SearchPath", ctrl: b.SearchPath, attach: true},
		{name: "StatementTimeout", ctrl: b.StatementTimeout, attach: true},
		{name: "QueryEditor", ctrl: b.QueryEditor, attach: true},
		{name: "Tx", ctrl: b.Tx, attach: true},
		{name: "ResultTabs", ctrl: b.ResultTabs, attach: true},
		{name: "HideOverlay", ctrl: b.HideOverlay, attach: true},
		{name: "ExportMenu", ctrl: b.ExportMenu, attach: true},
		{name: "VimEditor", ctrl: b.VimEditor, attach: false},
		{name: "Plan", ctrl: b.Plan, attach: true},
		{name: "TableInspect", ctrl: b.TableInspect, attach: true},
		{name: "CellEditor", ctrl: b.CellEditor, attach: true},
		{name: "CommitDialog", ctrl: b.CommitDialog, attach: true},
		{name: "ConflictDialog", ctrl: b.ConflictDialog, attach: true},
		{name: "FKReversePicker", ctrl: b.FKReversePicker, attach: true},
		{name: "Cheatsheet", ctrl: b.Cheatsheet, attach: true},
	}
	out := make([]controllerEntry, 0, len(candidates))
	for _, e := range candidates {
		// A nil *T stored in the interface is non-nil at the interface level;
		// reflect distinguishes the typed-nil so nil fields are skipped.
		if e.ctrl == nil || reflect.ValueOf(e.ctrl).IsNil() {
			continue
		}
		out = append(out, e)
	}
	return out
}

// RegisterActions registers every controller's action handlers with reg.
// The trait actions (ListUp / ListDown / ListConfirm) are registered
// per-rail — each rail's trait emits its own scope-suffixed ID
// (`list.down:CONNECTIONS`, `list.down:SCHEMAS`, …) so j/k/<CR> on
// rail X dispatch to rail X's cursor (dbsavvy-6m9). Pre-fix only the
// Connections trait registered handlers and every rail's j/k mutated
// the Connections cursor.
//
// Rail-switch actions are registered by the orchestrator via
// controllers.RegisterRailSwitchActions (it needs the focus tree +
// context registry which this aggregate does not hold).
//
// Subsequent re-registrations of the same ID are silently swallowed via
// commands.Registry.Register returning ErrDuplicateAction.
func (b *Controllers) RegisterActions(reg *commands.Registry) {
	if b == nil || reg == nil {
		return
	}
	// Per-rail trait actions (list.down:CONNECTIONS, …) are registered by
	// the ListControllerTrait embedded in each side-rail controller, which
	// the controller's own RegisterActions does NOT call. These three calls
	// stay explicit because the trait is a sub-object, not a Controllers
	// field, and so is not part of the entries() registry.
	if b.Connections != nil && b.Connections.ListControllerTrait != nil {
		b.Connections.ListControllerTrait.RegisterActions(reg)
	}
	if b.Schemas != nil && b.Schemas.ListControllerTrait != nil {
		b.Schemas.ListControllerTrait.RegisterActions(reg)
	}
	if b.Tables != nil && b.Tables.ListControllerTrait != nil {
		b.Tables.ListControllerTrait.RegisterActions(reg)
	}
	// Per-controller action handlers, derived from the single registry.
	// Duplicate-ID re-registrations are swallowed by the Registry, so the
	// declaration-order iteration here is behaviour-equivalent to the prior
	// hand-listed order.
	for _, e := range b.entries() {
		e.ctrl.RegisterActions(reg)
	}
}

// Null-picker fallbacks. Returning nil/empty from every accessor is the
// documented no-op sentinel honored by every controller handler.

type nullConnectionPicker struct{}

func (nullConnectionPicker) SelectedConnection() *models.Connection { return nil }

type nullSchemaPicker struct{}

func (nullSchemaPicker) SelectedSchemaName() string { return "" }
func (nullSchemaPicker) ToggleShowHidden()          {}

type nullTablePicker struct{}

func (nullTablePicker) SelectedTable() *models.Table { return nil }

type nullActiveConnection struct{}

func (nullActiveConnection) ActiveConnectionID() string { return "" }
