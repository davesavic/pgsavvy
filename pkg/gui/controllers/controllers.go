package controllers

import (
	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// Controllers is the bundle of every controller instance the gui owns.
// Returned by AttachControllers; T10 (bootstrap) keeps the bundle so
// individual controllers remain accessible after wiring completes.
type Controllers struct {
	Connections *ConnectionsController
	Schemas     *SchemasController
	Tables      *TablesController
	Columns     *ColumnsController
	Indexes     *IndexesController
	Menu        *MenuController
	Quit        *QuitController
	QueryEditor *QueryEditorController
	ResultTabs  *ResultTabsController
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

	columns := NewColumnsController(c, helpers, &tree.Columns.SideListContext)
	columns.AttachToContext(&tree.Columns.BaseContext)

	indexes := NewIndexesController(c, helpers, &tree.Indexes.SideListContext)
	indexes.AttachToContext(&tree.Indexes.BaseContext)

	menu := NewMenuController(c, helpers)
	menu.AttachToContext(&tree.Menu.BaseContext)

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

	return &Controllers{
		Connections: connections,
		Schemas:     schemas,
		Tables:      tables,
		Columns:     columns,
		Indexes:     indexes,
		Menu:        menu,
		Quit:        quit,
		QueryEditor: queryEditor,
		ResultTabs:  resultTabs,
	}
}

// RegisterActions registers every controller's action handlers with reg.
// The trait actions (ListUp / ListDown / ListConfirm) are registered once
// via the Connections controller's embedded trait so they exist in the
// Registry without each rail-controller fighting for the same IDs.
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
	if b.Columns != nil {
		b.Columns.RegisterActions(reg)
	}
	if b.Indexes != nil {
		b.Indexes.RegisterActions(reg)
	}
	if b.Menu != nil {
		b.Menu.RegisterActions(reg)
	}
	if b.QueryEditor != nil {
		b.QueryEditor.RegisterActions(reg)
	}
	if b.ResultTabs != nil {
		b.ResultTabs.RegisterActions(reg)
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
