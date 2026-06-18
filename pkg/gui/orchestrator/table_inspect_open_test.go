package orchestrator_test

import (
	"context"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// connectAndSelectTable wires the fake driver, fires Connect, and seeds
// the TABLES rail with a single *models.Table (selected by default).
// Returns the Gui plus the seeded table.
func connectAndSelectTable(t *testing.T, sch, name string) (*orchestrator.Gui, *models.Table) {
	t.Helper()
	g, _ := buildTestGuiWithHistory(t)

	caps := drivers.Capabilities{}
	driverName, conn := registerWireFake(t, caps)
	conn.columns = []models.Column{
		{Name: "id"},
		{Name: "email"},
	}
	conn.indexes = []models.Index{
		{Name: "users_pkey", Schema: sch, Table: name},
	}

	bag := g.HelperBagForTest()
	profile := &models.Connection{Name: "inspect", Driver: driverName, DSN: "postgres://stub"}
	if err := bag.Connect.Connect(context.Background(), profile); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	tbl := &models.Table{Schema: sch, Name: name}
	g.Registry().Tables.SetItems([]any{tbl})
	g.Registry().Tables.SetCursor(0)
	return g, tbl
}

// fireTableInspectOpen looks up the TableInspectOpen command and invokes
// its handler with a zero ExecCtx. Fails the test on a missing command.
func fireTableInspectOpen(t *testing.T, g *orchestrator.Gui) {
	t.Helper()
	cmd, ok := g.CommandRegistry().Get(commands.TableInspectOpen)
	if !ok || cmd == nil || cmd.Handler == nil {
		t.Fatalf("TableInspectOpen not registered or handler nil")
	}
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("TableInspectOpen handler: %v", err)
	}
}

func TestTableInspectOpen_PushesPopupWhenTableSelected(t *testing.T) {
	g, _ := connectAndSelectTable(t, "public", "users")

	fireTableInspectOpen(t, g)

	top := g.ContextTree().Current()
	if top == nil {
		t.Fatal("focus stack top is nil after TableInspectOpen")
	}
	if got := top.GetKey(); got != types.TABLE_INSPECT {
		t.Fatalf("focus stack top = %q, want %q", got, types.TABLE_INSPECT)
	}
	// SetTarget snapshot landed.
	if sch, name := g.Registry().TableInspect.Target(); sch != "public" || name != "users" {
		t.Fatalf("Target = (%q,%q), want (public,users)", sch, name)
	}
	// Open lands on the first (Columns) tab.
	if got := g.Registry().TableInspect.ActiveTab(); got != 0 {
		t.Fatalf("ActiveTab after open = %d, want 0", got)
	}
}

func TestTableInspectOpen_NoOpWhenNoSelection(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	// No SetItems → SelectedItem() returns nil.
	beforeDepth := len(g.ContextTree().Stack())

	fireTableInspectOpen(t, g)

	afterDepth := len(g.ContextTree().Stack())
	if afterDepth != beforeDepth {
		t.Fatalf("focus stack depth changed: before=%d after=%d (handler should be a no-op)", beforeDepth, afterDepth)
	}
}

func TestTableInspectOpen_DispatchesTwoWorkers(t *testing.T) {
	g, _ := connectAndSelectTable(t, "public", "users")

	before := g.OnWorkerCountForTest()
	fireTableInspectOpen(t, g)
	g.WaitForWorkersForTest()
	after := g.OnWorkerCountForTest()

	if got := after - before; got != 2 {
		t.Fatalf("OnWorker enqueue count = %d, want 2 (columns + indexes)", got)
	}
}

func TestTableInspectOpen_ClearsLoadingAfterBothRefresh(t *testing.T) {
	g, _ := connectAndSelectTable(t, "public", "users")

	fireTableInspectOpen(t, g)
	g.WaitForWorkersForTest()

	// done() schedules SetLoading(false) on OnUIThreadContentOnly, which
	// the recorder driver dispatches via Update. Assert directly.
	if g.Registry().TableInspect.IsLoading() {
		t.Fatal("TableInspect.IsLoading() = true after both refreshes; expected false")
	}
	if got := len(g.Registry().Columns.Items()); got != 2 {
		t.Fatalf("Columns.Items() = %d, want 2", got)
	}
	if got := len(g.Registry().Indexes.Items()); got != 1 {
		t.Fatalf("Indexes.Items() = %d, want 1", got)
	}
}

func TestTableInspectOpen_ReOpenReTargets(t *testing.T) {
	g, _ := connectAndSelectTable(t, "public", "users")

	fireTableInspectOpen(t, g)
	g.WaitForWorkersForTest()

	depthAfterFirst := len(g.ContextTree().Stack())
	if got := g.ContextTree().Current().GetKey(); got != types.TABLE_INSPECT {
		t.Fatalf("first open: top = %q, want %q", got, types.TABLE_INSPECT)
	}

	// Move off the first tab so the re-open's SetActiveTab(0) is observable.
	g.Registry().TableInspect.SetActiveTab(1)

	// Swap the selected table without closing the popup.
	tblB := &models.Table{Schema: "public", Name: "orders"}
	g.Registry().Tables.SetItems([]any{tblB})
	g.Registry().Tables.SetCursor(0)

	fireTableInspectOpen(t, g)
	g.WaitForWorkersForTest()

	depthAfterSecond := len(g.ContextTree().Stack())
	if depthAfterSecond != depthAfterFirst {
		t.Fatalf("re-open: focus stack depth changed (was %d, now %d) — expected re-target without Push",
			depthAfterFirst, depthAfterSecond)
	}
	if sch, name := g.Registry().TableInspect.Target(); sch != "public" || name != "orders" {
		t.Fatalf("Target after re-open = (%q,%q), want (public,orders)", sch, name)
	}
	// Re-open resets to the first (Columns) tab.
	if got := g.Registry().TableInspect.ActiveTab(); got != 0 {
		t.Fatalf("ActiveTab after re-open = %d, want 0", got)
	}
}
