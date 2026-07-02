package controllers

import (
	"context"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// --- minimal inline fakes (the shared fakes_test.go fakes live in the
// external controllers_test package and are not visible here). ---

type railSchemaPicker struct {
	name        string
	toggleCount int
}

func (f *railSchemaPicker) SelectedSchemaName() string { return f.name }
func (f *railSchemaPicker) ToggleShowHidden()          { f.toggleCount++ }

type railTablePicker struct{ sel *models.Table }

func (f *railTablePicker) SelectedTable() *models.Table { return f.sel }

type railRefresh struct {
	schemas int
	tables  []string
}

func (f *railRefresh) RefreshSchemas(context.Context) error { f.schemas++; return nil }
func (f *railRefresh) RefreshTables(_ context.Context, schema string) error {
	f.tables = append(f.tables, schema)
	return nil
}
func (f *railRefresh) RefreshColumns(context.Context, string, string) error { return nil }
func (f *railRefresh) RefreshIndexes(context.Context, string, string) error { return nil }

// railFixture builds a real SCHEMA_RAIL container with both leaves, the two
// leaf controllers wired to fakes, and the SchemaRailController under test.
type railFixture struct {
	rail        *guicontext.SchemaRailContext
	ctrl        *SchemaRailController
	reg         *commands.Registry
	refresh     *railRefresh
	schemaName  *railSchemaPicker
	tablePicker *railTablePicker
	schemaAct   []string
	tableAct    []*models.Table
}

func newRailFixture(t *testing.T) *railFixture {
	t.Helper()
	tree := guicontext.NewContextTree(types.ContextTreeDeps{})
	tree.Schemas.SetItems([]any{models.Schema{Name: "public"}, models.Schema{Name: "app"}})
	tree.Tables.SetItems([]any{models.Table{Schema: "public", Name: "users"}})

	f := &railFixture{
		rail:        tree.SchemaRail,
		refresh:     &railRefresh{},
		schemaName:  &railSchemaPicker{name: "public"},
		tablePicker: &railTablePicker{sel: &models.Table{Schema: "public", Name: "users"}},
	}

	nav := NavDeps{
		Schemas:          f.schemaName,
		Tables:           f.tablePicker,
		Refresh:          f.refresh,
		ActiveConnection: railActiveConn{},
		OnSchemaActivate: func(s string) { f.schemaAct = append(f.schemaAct, s) },
		OnTableActivate:  func(tbl *models.Table) error { f.tableAct = append(f.tableAct, tbl); return nil },
	}
	schemas := NewSchemasController(nil, CoreDeps{}, nav, UIDeps{}, &tree.Schemas.SideListContext, f.schemaName)
	tables := NewTablesController(nil, CoreDeps{}, nav, &tree.Tables.SideListContext, f.tablePicker)
	f.ctrl = NewSchemaRailController(newBase(nil, HelperBag{NavDeps: nav}), tree.SchemaRail, schemas, tables)

	f.reg = commands.NewRegistry()
	schemas.ListControllerTrait.RegisterActions(f.reg)
	tables.ListControllerTrait.RegisterActions(f.reg)
	schemas.RegisterActions(f.reg)
	tables.RegisterActions(f.reg)
	f.ctrl.RegisterActions(f.reg)
	return f
}

type railActiveConn struct{}

func (railActiveConn) ActiveConnectionID() string { return "local" }

func (f *railFixture) invoke(t *testing.T, id string) {
	t.Helper()
	cmd, ok := f.reg.Get(id)
	if !ok || cmd == nil || cmd.Handler == nil {
		t.Fatalf("action %q not registered", id)
	}
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("handler %q: %v", id, err)
	}
}

// TestSchemaRail_TabCycleWraps verifies ']' wraps last→first and '[' wraps
// first→last without panic.
func TestSchemaRail_TabCycleWraps(t *testing.T) {
	f := newRailFixture(t)

	// Default = Schemas (0). ']' → Tables (1) → wrap → Schemas (0).
	f.invoke(t, commands.RailTabNext)
	if got := f.rail.ActiveTab(); got != guicontext.SchemaRailTabTables {
		t.Fatalf("after ] from Schemas: tab=%d, want Tables(%d)", got, guicontext.SchemaRailTabTables)
	}
	f.invoke(t, commands.RailTabNext) // last → wrap → first
	if got := f.rail.ActiveTab(); got != guicontext.SchemaRailTabSchemas {
		t.Fatalf("after ] from Tables (wrap): tab=%d, want Schemas(%d)", got, guicontext.SchemaRailTabSchemas)
	}

	// '[' from first (Schemas) wraps to last (Tables).
	f.invoke(t, commands.RailTabPrev)
	if got := f.rail.ActiveTab(); got != guicontext.SchemaRailTabTables {
		t.Fatalf("after [ from Schemas (wrap): tab=%d, want Tables(%d)", got, guicontext.SchemaRailTabTables)
	}
	f.invoke(t, commands.RailTabPrev) // Tables → Schemas
	if got := f.rail.ActiveTab(); got != guicontext.SchemaRailTabSchemas {
		t.Fatalf("after [ from Tables: tab=%d, want Schemas(%d)", got, guicontext.SchemaRailTabSchemas)
	}
}

// TestSchemaRail_ConfirmDispatchesByTab verifies <CR> hits the active leaf's
// behaviour: Schemas tab → OnSchemaActivate; Tables tab → OnTableActivate.
func TestSchemaRail_ConfirmDispatchesByTab(t *testing.T) {
	f := newRailFixture(t)

	// Schemas tab.
	f.invoke(t, commands.SchemaRailConfirm)
	if len(f.schemaAct) != 1 || f.schemaAct[0] != "public" {
		t.Fatalf("Schemas tab <CR>: schemaAct=%v, want [public]", f.schemaAct)
	}
	if len(f.tableAct) != 0 {
		t.Fatalf("Schemas tab <CR> wrongly fired OnTableActivate: %v", f.tableAct)
	}

	// Tables tab.
	f.rail.SetActiveTab(guicontext.SchemaRailTabTables)
	f.invoke(t, commands.SchemaRailConfirm)
	if len(f.tableAct) != 1 {
		t.Fatalf("Tables tab <CR>: tableAct len=%d, want 1", len(f.tableAct))
	}
	if len(f.schemaAct) != 1 {
		t.Fatalf("Tables tab <CR> wrongly fired OnSchemaActivate again: %v", f.schemaAct)
	}
}

// TestSchemaRail_RefreshDispatchesByTab verifies `r` hits the active leaf's
// refresh: Schemas tab → RefreshSchemas; Tables tab → RefreshTables.
func TestSchemaRail_RefreshDispatchesByTab(t *testing.T) {
	f := newRailFixture(t)

	f.invoke(t, commands.SchemaRailRefresh) // Schemas tab
	if f.refresh.schemas != 1 || len(f.refresh.tables) != 0 {
		t.Fatalf("Schemas tab r: schemas=%d tables=%v, want schemas=1 tables=[]", f.refresh.schemas, f.refresh.tables)
	}

	f.rail.SetActiveTab(guicontext.SchemaRailTabTables)
	f.invoke(t, commands.SchemaRailRefresh) // Tables tab
	if f.refresh.schemas != 1 || len(f.refresh.tables) != 1 || f.refresh.tables[0] != "public" {
		t.Fatalf("Tables tab r: schemas=%d tables=%v, want schemas=1 tables=[public]", f.refresh.schemas, f.refresh.tables)
	}
}

// TestSchemaRail_TabUniqueChordsNoopOnWrongTab verifies H (Schemas-only) is a
// no-op on the Tables tab and i (Tables-only) is a no-op on the Schemas tab.
func TestSchemaRail_TabUniqueChordsNoopOnWrongTab(t *testing.T) {
	f := newRailFixture(t)

	// toggle-show-hidden (<leader>H) is Schemas-only.
	f.rail.SetActiveTab(guicontext.SchemaRailTabTables)
	f.invoke(t, commands.SchemaRailToggleShowHidden)
	if f.schemaName.toggleCount != 0 {
		t.Fatalf("toggle-show-hidden fired on Tables tab; count=%d want 0", f.schemaName.toggleCount)
	}
	f.rail.SetActiveTab(guicontext.SchemaRailTabSchemas)
	f.invoke(t, commands.SchemaRailToggleShowHidden)
	if f.schemaName.toggleCount != 1 {
		t.Fatalf("toggle-show-hidden no-op on Schemas tab; count=%d want 1", f.schemaName.toggleCount)
	}

	// inspect (i) is Tables-only and forwards to TableInspectOpen. Register a
	// spy so we can observe whether it was invoked.
	var inspectCalls int
	_ = f.reg.Register(&commands.Command{
		ID:      commands.TableInspectOpen,
		Handler: func(commands.ExecCtx) error { inspectCalls++; return nil },
	})
	// On Schemas tab: i must NOT forward.
	f.rail.SetActiveTab(guicontext.SchemaRailTabSchemas)
	f.invoke(t, commands.SchemaRailInspect)
	if inspectCalls != 0 {
		t.Fatalf("inspect forwarded on Schemas tab; calls=%d want 0", inspectCalls)
	}
	// On Tables tab: i forwards.
	f.rail.SetActiveTab(guicontext.SchemaRailTabTables)
	f.invoke(t, commands.SchemaRailInspect)
	if inspectCalls != 1 {
		t.Fatalf("inspect did not forward on Tables tab; calls=%d want 1", inspectCalls)
	}
}

// TestSchemaRail_NavDrivesActiveLeafCursor verifies j/k move the ACTIVE leaf's
// cursor (tab-agnostic).
func TestSchemaRail_NavDrivesActiveLeafCursor(t *testing.T) {
	f := newRailFixture(t)

	// Schemas tab cursor starts at 0; SchemaRailDown advances it.
	leaf := f.rail.ActiveLeaf()
	if leaf == nil {
		t.Fatal("ActiveLeaf nil")
	}
	start := leaf.Cursor()
	f.invoke(t, commands.SchemaRailDown)
	if got := f.rail.ActiveLeaf().Cursor(); got != start+1 {
		t.Fatalf("SchemaRailDown on Schemas: cursor=%d, want %d", got, start+1)
	}
}

// TestSchemaRailPublishesRailSwitchBindings asserts the SCHEMA_RAIL container
// publishes the `3`→QueryEditor / `4`→Results / Tab→Next rail-switch triple
// under its OWN scope.
func TestSchemaRailPublishesRailSwitchBindings(t *testing.T) {
	f := newRailFixture(t)
	got := f.ctrl.GetKeybindings(types.KeybindingsOpts{})

	want := map[string]struct {
		key     rune
		special types.SpecialKey
		action  string
	}{
		"3":    {'3', types.KeyNone, commands.RailSwitchQueryEditor},
		"4":    {'4', types.KeyNone, commands.RailSwitchResults},
		"<tab>": {0, types.KeyTab, commands.RailSwitchNext},
	}
	for _, b := range got {
		if len(b.Sequence) != 1 {
			continue
		}
		k := b.Sequence[0]
		var label string
		switch {
		case k.Special == types.KeyNone && k.Code >= '0' && k.Code <= '9':
			label = string(k.Code)
		case k.Special == types.KeyTab:
			label = "<tab>"
		default:
			continue
		}
		exp, ok := want[label]
		if !ok {
			continue
		}
		if b.ActionID != exp.action {
			t.Errorf("%q action = %q, want %q", label, b.ActionID, exp.action)
		}
		if b.Scope != types.SCHEMA_RAIL {
			t.Errorf("%q scope = %s, want %s", label, b.Scope, types.SCHEMA_RAIL)
		}
		if b.Mode != types.ModeNormal {
			t.Errorf("%q mode = %v, want Normal", label, b.Mode)
		}
		delete(want, label)
	}
	for label := range want {
		t.Errorf("missing binding for %q", label)
	}
}
