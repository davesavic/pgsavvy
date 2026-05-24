package controllers

import (
	"time"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
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

	connections := NewConnectionsController(c, helpers, &tree.Connections.SideListContext, helpers.Connections)
	connections.AttachToContext(&tree.Connections.BaseContext)

	schemas := NewSchemasController(c, helpers, &tree.Schemas.SideListContext, helpers.Schemas)
	schemas.AttachToContext(&tree.Schemas.BaseContext)

	tables := NewTablesController(c, helpers, &tree.Tables.SideListContext, helpers.Tables)
	tables.AttachToContext(&tree.Tables.BaseContext)

	menu := NewMenuController(c, helpers)
	menu.AttachToContext(&tree.Menu.BaseContext)

	prompt := NewPromptController(c, helpers)
	prompt.AttachToContext(&tree.Prompt.BaseContext)

	selection := NewSelectionController(c, helpers)
	selection.AttachToContext(&tree.Selection.BaseContext)

	confirmation := NewConfirmationController(c, helpers)
	confirmation.AttachToContext(&tree.Confirmation.BaseContext)

	quit := NewQuitController(c, helpers)
	quit.AttachToContext(&tree.Global.BaseContext)

	queryEditor := NewQueryEditorController(c, helpers)
	// tree.QueryEditor is a StubContext today (dbsavvy-66p.11);
	// AddKeybindingsFn is a no-op there. The bindings reach the trie
	// via AllDefaultBindings until the live QUERY_EDITOR context
	// ships in a later epic. AttachToContext is still called so the
	// wiring lights up automatically once the stub is replaced.
	queryEditor.AttachToContext(tree.QueryEditor)

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
	resultTabs := NewResultTabsController(c, helpers, tabsMgr)
	resultTabs.AttachToContext(tree.ResultGrid)

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
	hideOverlay := NewHideOverlayController(c, helpers, hideMgr)
	hideOverlay.AttachToContext(&tree.HideOverlay.BaseContext)

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
	exportMenu := NewExportMenuController(c, helpers, exportMgr)
	exportMenu.AttachToContext(&tree.ExportMenu.BaseContext)

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
		// No AttachToContext: VimEditor bindings reach the trie via
		// AllDefaultBindings, mirroring ResultTabsController's path
		// (see controllers.go:98-100). The Matcher routes keystrokes
		// to the QUERY_EDITOR scope based on the focused context, so
		// no per-context AddKeybindingsFn call is required.
	}

	// PlanController publishes PLAN-scoped tree-navigation bindings
	// (dbsavvy-uv0.8). The plan tab's per-tab *context.PlanContext is
	// reached through helpers.ActivePlanContextFn (wired by the
	// orchestrator to ResultTabsHelper.ActivePlanContext). The
	// controller is attached to tree.Plan even though that's a
	// StubContext today — AttachToContext on a stub is a no-op, and
	// the bindings reach the trie via AllDefaultBindings.
	plan := NewPlanController(c, helpers, helpers.ActivePlanContextFn)
	plan.AttachToContext(tree.Plan)

	return &Controllers{
		Connections:  connections,
		Schemas:      schemas,
		Tables:       tables,
		Menu:         menu,
		Prompt:       prompt,
		Selection:    selection,
		Confirmation: confirmation,
		Quit:         quit,
		QueryEditor:  queryEditor,
		ResultTabs:   resultTabs,
		HideOverlay:  hideOverlay,
		ExportMenu:   exportMenu,
		VimEditor:    vimEditor,
		Plan:         plan,
	}
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
	if b.Connections != nil && b.Connections.ListControllerTrait != nil {
		b.Connections.ListControllerTrait.RegisterActions(reg)
	}
	if b.Schemas != nil && b.Schemas.ListControllerTrait != nil {
		b.Schemas.ListControllerTrait.RegisterActions(reg)
	}
	if b.Tables != nil && b.Tables.ListControllerTrait != nil {
		b.Tables.ListControllerTrait.RegisterActions(reg)
	}
	if b.Quit != nil {
		b.Quit.RegisterActions(reg)
	}
	if b.Connections != nil {
		b.Connections.RegisterActions(reg)
	}
	if b.Schemas != nil {
		b.Schemas.RegisterActions(reg)
	}
	if b.Tables != nil {
		b.Tables.RegisterActions(reg)
	}
	if b.Menu != nil {
		b.Menu.RegisterActions(reg)
	}
	if b.Prompt != nil {
		b.Prompt.RegisterActions(reg)
	}
	if b.Selection != nil {
		b.Selection.RegisterActions(reg)
	}
	if b.Confirmation != nil {
		b.Confirmation.RegisterActions(reg)
	}
	if b.QueryEditor != nil {
		b.QueryEditor.RegisterActions(reg)
	}
	if b.ResultTabs != nil {
		b.ResultTabs.RegisterActions(reg)
	}
	if b.HideOverlay != nil {
		b.HideOverlay.RegisterActions(reg)
	}
	if b.ExportMenu != nil {
		b.ExportMenu.RegisterActions(reg)
	}
	if b.VimEditor != nil {
		b.VimEditor.RegisterActions(reg)
	}
	if b.Plan != nil {
		b.Plan.RegisterActions(reg)
	}
	if b.TableInspect != nil {
		b.TableInspect.RegisterActions(reg)
	}
	if b.CellEditor != nil {
		b.CellEditor.RegisterActions(reg)
	}
	if b.CommitDialog != nil {
		b.CommitDialog.RegisterActions(reg)
	}
	if b.ConflictDialog != nil {
		b.ConflictDialog.RegisterActions(reg)
	}
	if b.FKReversePicker != nil {
		b.FKReversePicker.RegisterActions(reg)
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
