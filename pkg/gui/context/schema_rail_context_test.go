package context

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// railTestDriver records SetViewTabs calls and SetContent payloads, and
// hands back a single real *gocui.View from ViewByName so the container's
// per-tab origin save/restore can be exercised against a live origin.
type railTestDriver struct {
	stubDriver
	view *gocui.View

	tabsCalls   []railTabsCall
	lastView    string
	lastContent string
}

type railTabsCall struct {
	name   string
	labels []string
	active int
}

func (d *railTestDriver) Update(fn func() error) { _ = fn() }

func (d *railTestDriver) SetContent(view, str string) error {
	d.lastView = view
	d.lastContent = str
	return nil
}

func (d *railTestDriver) ViewByName(string) (types.View, error) { return d.view, nil }

func (d *railTestDriver) SetViewTabs(name string, labels []string, active int) error {
	d.tabsCalls = append(d.tabsCalls, railTabsCall{name: name, labels: labels, active: active})
	return nil
}

// newRailTree wires a real ContextTree (so the container + leaves are
// constructed and SetLeaves runs) bound to the supplied driver, and seeds
// each leaf with a couple of rows.
func newRailTree(drv types.GuiDriver) *ContextTree {
	tree := NewContextTree(types.ContextTreeDeps{GuiDriver: drv})
	tree.Schemas.SetItems([]any{
		models.Schema{Name: "public"},
		models.Schema{Name: "app"},
	})
	tree.Tables.SetItems([]any{
		models.Table{Name: "users"},
	})
	return tree
}

func TestSchemaRail_DefaultsToSchemasTab(t *testing.T) {
	tree := newRailTree(&railTestDriver{})
	if got := tree.SchemaRail.ActiveTab(); got != SchemaRailTabSchemas {
		t.Fatalf("default active tab = %d, want %d (Schemas)", got, SchemaRailTabSchemas)
	}
	if leaf := tree.SchemaRail.ActiveLeaf(); leaf != &tree.Schemas.SideListContext {
		t.Fatal("ActiveLeaf() did not return the Schemas leaf at default")
	}
}

func TestSchemaRail_ViewNameIsSharedView(t *testing.T) {
	tree := newRailTree(&railTestDriver{})
	for _, c := range []types.IBaseContext{tree.SchemaRail, tree.Schemas, tree.Tables} {
		if got := c.GetViewName(); got != "schemas-tables" {
			t.Errorf("%s.GetViewName() = %q, want %q", c.GetKey(), got, "schemas-tables")
		}
	}
}

func TestSchemaRail_PublishesTabsEveryFrameWithActiveIndex(t *testing.T) {
	drv := &railTestDriver{}
	tree := newRailTree(drv)

	if err := tree.SchemaRail.HandleRender(); err != nil {
		t.Fatalf("HandleRender (schemas): %v", err)
	}
	tree.SchemaRail.SetActiveTab(SchemaRailTabTables)
	if err := tree.SchemaRail.HandleRender(); err != nil {
		t.Fatalf("HandleRender (tables): %v", err)
	}

	if len(drv.tabsCalls) != 2 {
		t.Fatalf("SetViewTabs called %d times, want 2 (once per frame)", len(drv.tabsCalls))
	}
	for _, c := range drv.tabsCalls {
		if c.name != "schemas-tables" {
			t.Errorf("SetViewTabs name = %q, want schemas-tables", c.name)
		}
		if len(c.labels) != 2 {
			t.Errorf("SetViewTabs labels = %v, want 2 labels", c.labels)
		}
	}
	// Frame 1: Schemas active → "[Schemas]", "Tables".
	if got := drv.tabsCalls[0].labels; got[0] != "[Schemas]" || got[1] != "Tables" {
		t.Errorf("frame 1 labels = %v, want [[Schemas] Tables] (active marker on Schemas)", got)
	}
	if drv.tabsCalls[0].active != SchemaRailTabSchemas {
		t.Errorf("frame 1 active = %d, want %d", drv.tabsCalls[0].active, SchemaRailTabSchemas)
	}
	// Frame 2: Tables active → "Schemas", "[Tables]".
	if got := drv.tabsCalls[1].labels; got[0] != "Schemas" || got[1] != "[Tables]" {
		t.Errorf("frame 2 labels = %v, want [Schemas [Tables]] (active marker on Tables)", got)
	}
	if drv.tabsCalls[1].active != SchemaRailTabTables {
		t.Errorf("frame 2 active = %d, want %d", drv.tabsCalls[1].active, SchemaRailTabTables)
	}
}

func TestSchemaRail_RendersOnlyActiveLeaf(t *testing.T) {
	drv := &railTestDriver{}
	tree := newRailTree(drv)

	// Schemas tab active: the body must contain a schema row, not a table row.
	if err := tree.SchemaRail.HandleRender(); err != nil {
		t.Fatalf("HandleRender (schemas): %v", err)
	}
	if drv.lastView != "schemas-tables" {
		t.Fatalf("active render wrote view %q, want schemas-tables", drv.lastView)
	}
	if !containsRow(drv.lastContent, "public") {
		t.Errorf("schemas-tab body = %q, want a 'public' schema row", drv.lastContent)
	}
	if containsRow(drv.lastContent, "users") {
		t.Errorf("schemas-tab body leaked a table row: %q", drv.lastContent)
	}

	// Switch to Tables: now the body must be the table row, not a schema row.
	tree.SchemaRail.SetActiveTab(SchemaRailTabTables)
	if err := tree.SchemaRail.HandleRender(); err != nil {
		t.Fatalf("HandleRender (tables): %v", err)
	}
	if !containsRow(drv.lastContent, "users") {
		t.Errorf("tables-tab body = %q, want a 'users' table row", drv.lastContent)
	}
	if containsRow(drv.lastContent, "public") {
		t.Errorf("tables-tab body leaked a schema row: %q", drv.lastContent)
	}
}

func TestSchemaRail_InactiveLeafRenderIsNoOpAgainstSharedView(t *testing.T) {
	drv := &railTestDriver{}
	tree := newRailTree(drv)

	// Active = Schemas. Calling the INACTIVE Tables leaf's HandleRender
	// directly would write the shared view, but the container never does so
	// — only the active leaf renders. Prove the container's render path keeps
	// the inactive leaf out of the shared view by asserting the body after a
	// container render is the active (Schemas) content even though the Tables
	// leaf has rows.
	if err := tree.SchemaRail.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if containsRow(drv.lastContent, "users") {
		t.Fatalf("inactive Tables leaf bled into the shared view: %q", drv.lastContent)
	}
}

func TestSchemaRail_PerTabOriginDoesNotBleed(t *testing.T) {
	v := gocui.NewView("schemas-tables", 0, 0, 20, 10, gocui.OutputNormal)
	drv := &railTestDriver{view: v}
	tree := newRailTree(drv)

	// Active = Schemas. Simulate a horizontal pan on tab A (ox = 5).
	v.SetOrigin(5, 0)

	// Switch to Tables: the switch captures A's origin; the incoming Tables
	// tab starts at its saved origin (0,0).
	tree.SchemaRail.SetActiveTab(SchemaRailTabTables)
	if err := tree.SchemaRail.HandleRender(); err != nil {
		t.Fatalf("HandleRender (tables): %v", err)
	}
	if ox, _ := v.Origin(); ox != 0 {
		t.Errorf("tables tab ox = %d, want 0 (no bleed from schemas pan)", ox)
	}

	// Switch back to Schemas: its saved horizontal pan (ox = 5) is restored.
	tree.SchemaRail.SetActiveTab(SchemaRailTabSchemas)
	if err := tree.SchemaRail.HandleRender(); err != nil {
		t.Fatalf("HandleRender (schemas restore): %v", err)
	}
	if ox, _ := v.Origin(); ox != 5 {
		t.Errorf("schemas tab ox = %d after switch-back, want 5 (origin preserved)", ox)
	}
}

// railDeferredDriver models production GuiDriver timing: Update ENQUEUES the
// callback (the leaf's SetContent and FocusPoint) to run on a later loop tick
// instead of inline. railTestDriver runs Update synchronously, which hides the
// per-frame origin clobber because the leaf's FocusPoint scroll-to-cursor lands
// before the assertion; deferring it exposes the origin the synchronous restore
// in HandleRender leaves behind at draw time — exactly as gocui draws.
type railDeferredDriver struct {
	stubDriver
	view  *gocui.View
	queue []func() error
}

func (d *railDeferredDriver) Update(fn func() error) { d.queue = append(d.queue, fn) }

func (d *railDeferredDriver) SetContent(_, str string) error {
	d.view.SetContent(str)
	return nil
}

func (d *railDeferredDriver) ViewByName(string) (types.View, error) { return d.view, nil }

// drain runs the queued Update callbacks in order, like the gocui main loop.
func (d *railDeferredDriver) drain() {
	q := d.queue
	d.queue = nil
	for _, fn := range q {
		_ = fn()
	}
}

// TestSchemaRail_ScrollFollowsCursorAcrossFrames is a regression test for the
// per-frame origin clobber. HandleRender restores the saved per-tab origin
// SYNCHRONOUSLY, but the leaf's vertical scroll-to-cursor (FocusPoint) runs in
// the DEFERRED Update queue. Once FocusPoint has scrolled the view to keep the
// cursor visible, the NEXT frame's restore must not yank oy back to the stale
// saved origin (only written on a tab switch) — otherwise the list never
// follows the cursor (the reported bug: rows do not scroll with the cursor).
func TestSchemaRail_ScrollFollowsCursorAcrossFrames(t *testing.T) {
	v := gocui.NewView("schemas-tables", 0, 0, 20, 10, gocui.OutputNormal)
	drv := &railDeferredDriver{view: v}
	tree := newRailTree(drv)

	rows := make([]any, 30)
	for i := range rows {
		rows[i] = models.Schema{Name: fmt.Sprintf("schema_%02d", i)}
	}
	tree.Schemas.SetItems(rows)
	tree.Schemas.SetCursor(29) // bottom row, well past the viewport

	// Frame 1: render queues SetContent + FocusPoint; draining scrolls oy down.
	if err := tree.SchemaRail.HandleRender(); err != nil {
		t.Fatalf("HandleRender frame 1: %v", err)
	}
	drv.drain()
	_, oy1 := v.Origin()
	if oy1 == 0 {
		t.Fatalf("frame 1: oy=0, cursor at row 29 never scrolled into view")
	}

	// Frame 2: NO tab switch. The synchronous restore at the top of HandleRender
	// must not reset oy back to the stale saved origin (0).
	if err := tree.SchemaRail.HandleRender(); err != nil {
		t.Fatalf("HandleRender frame 2: %v", err)
	}
	if _, oy2 := v.Origin(); oy2 != oy1 {
		t.Fatalf("frame 2 restore clobbered cursor scroll: oy=%d, want %d "+
			"(oy must follow the cursor, not the stale per-tab origin)", oy2, oy1)
	}
}

func TestSchemaRail_SetActiveTabClamps(t *testing.T) {
	tree := newRailTree(&railTestDriver{})
	tree.SchemaRail.SetActiveTab(99)
	if got := tree.SchemaRail.ActiveTab(); got != SchemaRailTabTables {
		t.Errorf("SetActiveTab(99) => %d, want clamp to %d", got, SchemaRailTabTables)
	}
	tree.SchemaRail.SetActiveTab(-3)
	if got := tree.SchemaRail.ActiveTab(); got != SchemaRailTabSchemas {
		t.Errorf("SetActiveTab(-3) => %d, want clamp to %d", got, SchemaRailTabSchemas)
	}
}

// containsRow reports whether body contains name on a rendered rail row
// (rows carry a "> "/"  " gutter marker, so a plain substring search
// suffices).
func containsRow(body, name string) bool {
	return strings.Contains(body, name)
}
