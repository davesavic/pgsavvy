package orchestrator_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/pgsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// inspectViewName is the view both inspect leaves render into.
const inspectViewName = "table-inspect"

// newInspectDriver returns a RecorderGuiDriver with the inspect view
// pre-created so the leaf contexts' SetContent writes land in a buffer the
// test can read back via GetViewBuffer.
func newInspectDriver(t *testing.T) *testfake.RecorderGuiDriver {
	t.Helper()
	drv := testfake.NewRecorderGuiDriver()
	// SetView returns ErrUnknownView on first creation (gocui semantics);
	// the view is registered regardless, which is all we need for SetContent.
	_, _ = drv.SetView(inspectViewName, 0, 0, 10, 10, 0)
	return drv
}

func newTestForeignKeysContext(drv types.GuiDriver) *guicontext.ForeignKeysContext {
	base := guicontext.NewBaseContext(guicontext.BaseContextOpts{
		Key:      types.FOREIGN_KEYS,
		ViewName: inspectViewName,
		Kind:     types.SIDE_CONTEXT,
	})
	return guicontext.NewForeignKeysContext(base, types.ContextTreeDeps{GuiDriver: drv})
}

func newTestConstraintsContext(drv types.GuiDriver) *guicontext.ConstraintsContext {
	base := guicontext.NewBaseContext(guicontext.BaseContextOpts{
		Key:      types.CONSTRAINTS,
		ViewName: inspectViewName,
		Kind:     types.SIDE_CONTEXT,
	})
	return guicontext.NewConstraintsContext(base, types.ContextTreeDeps{GuiDriver: drv})
}

// connectWireFake registers a wire fake driver, applies opt to its conn,
// then connects the test Gui through it. Returns the connected Gui.
func connectWireFake(t *testing.T, opt func(c *wireFakeConn)) *orchestrator.Gui {
	t.Helper()
	g, _ := buildTestGuiWithHistory(t)
	driverName, conn := registerWireFake(t, drivers.Capabilities{})
	if opt != nil {
		opt(conn)
	}
	profile := &models.Connection{Name: "inspect", Driver: driverName, DSN: "postgres://stub"}
	if err := g.HelperBagForTest().Connect.Connect(context.Background(), profile); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return g
}

// TestPopulateConstraintsRailFiltersCheckUnique asserts the populate worker
// keeps only CHECK and UNIQUE constraints, dropping PRIMARY KEY / FOREIGN KEY.
func TestPopulateConstraintsRailFiltersCheckUnique(t *testing.T) {
	g := connectWireFake(t, func(c *wireFakeConn) {
		c.constraints = []models.Constraint{
			{Name: "pk", Kind: "PRIMARY KEY", Definition: "PRIMARY KEY (id)"},
			{Name: "fk", Kind: "FOREIGN KEY", Definition: "FOREIGN KEY (a) REFERENCES t(id)"},
			{Name: "chk", Kind: "CHECK", Definition: "CHECK (n > 0)"},
			{Name: "uq", Kind: "UNIQUE", Definition: "UNIQUE (email)"},
		}
	})
	drv := newInspectDriver(t)
	conCtx := newTestConstraintsContext(drv)

	g.PopulateConstraintsRailForTest(conCtx, "public", "users")
	if err := conCtx.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.GetViewBuffer(inspectViewName)

	if !strings.Contains(body, "CHECK") || !strings.Contains(body, "UNIQUE") {
		t.Errorf("expected CHECK and UNIQUE rows, got: %q", body)
	}
	if strings.Contains(body, "PRIMARY KEY") || strings.Contains(body, "FOREIGN KEY") {
		t.Errorf("PRIMARY KEY / FOREIGN KEY must be filtered out, got: %q", body)
	}
}

// TestPopulateForeignKeysRailInboundErrorSetsOnlyInbound asserts a partial
// failure (outbound ok, inbound errors) sets the outbound rows and flags ONLY
// the inbound error line.
func TestPopulateForeignKeysRailInboundErrorSetsOnlyInbound(t *testing.T) {
	g := connectWireFake(t, func(c *wireFakeConn) {
		c.outboundFKs = []models.ForeignKey{{
			Columns: []string{"author_id"}, RefSchema: "public", RefTable: "users",
			RefColumns: []string{"id"}, OnDelete: "NO ACTION", OnUpdate: "NO ACTION",
		}}
		c.inboundFKErr = errors.New("boom")
	})
	drv := newInspectDriver(t)
	fkCtx := newTestForeignKeysContext(drv)

	g.PopulateForeignKeysRailForTest(fkCtx, "public", "posts")
	if err := fkCtx.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.GetViewBuffer(inspectViewName)

	if !strings.Contains(body, "author_id -> public.users(id)") {
		t.Errorf("outbound row must still render: %q", body)
	}
	if !strings.Contains(body, "could not load inbound foreign keys") {
		t.Errorf("inbound error line must be pinned: %q", body)
	}
	if strings.Contains(body, "could not load outbound foreign keys") {
		t.Errorf("outbound error line must NOT be set: %q", body)
	}
}

// TestPopulateForeignKeysRailEmptyClearsStaleItems asserts an empty result
// renders the empty-state placeholder for both directions (no stale data).
func TestPopulateForeignKeysRailEmptyClearsStaleItems(t *testing.T) {
	g := connectWireFake(t, nil) // no FKs configured -> empty
	drv := newInspectDriver(t)
	fkCtx := newTestForeignKeysContext(drv)
	// Seed stale prior-table data; populate must overwrite it.
	fkCtx.SetForeignKeys(
		[]models.ForeignKey{{Columns: []string{"stale"}, RefSchema: "public", RefTable: "x", RefColumns: []string{"id"}}},
		[]models.ForeignKey{{Columns: []string{"stale2"}, RefSchema: "public", RefTable: "y", RefColumns: []string{"id"}}},
	)

	g.PopulateForeignKeysRailForTest(fkCtx, "public", "fresh")
	if err := fkCtx.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.GetViewBuffer(inspectViewName)

	if strings.Contains(body, "stale") {
		t.Errorf("stale items must be cleared: %q", body)
	}
	if !strings.Contains(body, "No foreign keys") {
		t.Errorf("expected empty-state placeholder: %q", body)
	}
}

// TestPopulateConstraintsRailEmptyClearsStaleItems mirrors the FK empty case
// for the constraints leaf.
func TestPopulateConstraintsRailEmptyClearsStaleItems(t *testing.T) {
	g := connectWireFake(t, nil) // no constraints -> empty
	drv := newInspectDriver(t)
	conCtx := newTestConstraintsContext(drv)
	conCtx.SetConstraints([]models.Constraint{{Kind: "CHECK", Definition: "CHECK (stale > 0)"}})

	g.PopulateConstraintsRailForTest(conCtx, "public", "fresh")
	if err := conCtx.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.GetViewBuffer(inspectViewName)

	if strings.Contains(body, "stale") {
		t.Errorf("stale items must be cleared: %q", body)
	}
	if !strings.Contains(body, "No constraints") {
		t.Errorf("expected empty-state placeholder: %q", body)
	}
}

// TestPopulateForeignKeysRailRequireSessionErrorSetsError asserts that with no
// active session (never connected) both directions pin their error lines and
// the function returns cleanly.
func TestPopulateForeignKeysRailRequireSessionErrorSetsError(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t) // never Connect -> requireSession fails
	drv := newInspectDriver(t)
	fkCtx := newTestForeignKeysContext(drv)

	g.PopulateForeignKeysRailForTest(fkCtx, "public", "users")
	if err := fkCtx.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.GetViewBuffer(inspectViewName)

	if !strings.Contains(body, "could not load outbound foreign keys") {
		t.Errorf("outbound error line must be pinned: %q", body)
	}
	if !strings.Contains(body, "could not load inbound foreign keys") {
		t.Errorf("inbound error line must be pinned: %q", body)
	}
}

// TestPopulateConstraintsRailRequireSessionErrorSetsError asserts the
// constraints leaf pins its error line when no session is available.
func TestPopulateConstraintsRailRequireSessionErrorSetsError(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t) // never Connect
	drv := newInspectDriver(t)
	conCtx := newTestConstraintsContext(drv)

	g.PopulateConstraintsRailForTest(conCtx, "public", "users")
	if err := conCtx.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.GetViewBuffer(inspectViewName)

	if !strings.Contains(body, "could not load constraints") {
		t.Errorf("constraints error line must be pinned: %q", body)
	}
}
