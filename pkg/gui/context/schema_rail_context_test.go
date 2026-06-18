package context

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
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

// newSchemaRailWithSpies builds a SchemaRailContext directly (NOT via the
// ContextTree) and injects two fakeLeaf spies (Schemas index 0, Tables index 1)
// so leaf focus-hook calls can be counted across a real tab switch. The real
// tree wires concrete SchemasContext/TablesContext leaves that cannot count
// hooks; this standalone build lets the spies observe the FireFocusHooks=false
// policy. Concurrency N/A: single UI thread.
func newSchemaRailWithSpies(drv types.GuiDriver) (*SchemaRailContext, *fakeLeaf, *fakeLeaf) {
	base := NewBaseContext(BaseContextOpts{
		Key:      types.SCHEMA_RAIL,
		ViewName: SchemaRailViewName,
		Kind:     types.SIDE_CONTEXT,
	})
	rail := NewSchemaRailContext(base, Deps{GuiDriver: drv})
	schemas := newFakeLeaf(types.SCHEMAS)
	tables := newFakeLeaf(types.TABLES)
	rail.SetLeaves(schemas, tables)
	return rail, schemas, tables
}

// TestSchemaRail_TabSwitchFiresZeroLeafFocusHooks locks the SCHEMA_RAIL
// FireFocusHooks=FALSE policy: a REAL tab switch (Schemas -> Tables) fires
// ZERO leaf HandleFocus / HandleFocusLost on either leaf. (Contrast the
// QUERY_RAIL guard TestQueryRail_RealSwitchFiresHooksExactlyOnce, which fires
// exactly one of each.) Coverage item 1.
func TestSchemaRail_TabSwitchFiresZeroLeafFocusHooks(t *testing.T) {
	rail, schemas, tables := newSchemaRailWithSpies(&railTestDriver{})

	rail.SetActiveTab(SchemaRailTabTables) // real switch

	if rail.ActiveTab() != SchemaRailTabTables {
		t.Fatalf("active tab = %d after switch, want %d (switch still happens)", rail.ActiveTab(), SchemaRailTabTables)
	}
	if schemas.focusCount != 0 || schemas.focusLostCount != 0 ||
		tables.focusCount != 0 || tables.focusLostCount != 0 {
		t.Errorf("FireFocusHooks=false fired leaf hooks on tab switch: "+
			"schemas f=%d fl=%d, tables f=%d fl=%d, want all 0",
			schemas.focusCount, schemas.focusLostCount, tables.focusCount, tables.focusLostCount)
	}
}

// TestSchemaRail_ContainerFocusFiresNoSideEffectsForCurrentLeaves is the
// highest-risk silent-change guard (coverage item 2). The promoted CONTAINER
// HandleFocus AND HandleFocusLost run against the REAL Schemas/Tables leaves,
// which implement NEITHER a focus hook NOR the dirtyFlusher seam today. Both
// container hooks must therefore complete without error and produce zero
// side-effecting calls (no panic, no SetContent into the shared view). This is
// SEPARATE from the tab-switch policy test above: it exercises the container's
// own always-delegate/always-flush path, not the gated tab-switch path.
func TestSchemaRail_ContainerFocusFiresNoSideEffectsForCurrentLeaves(t *testing.T) {
	drv := &railTestDriver{}
	tree := newRailTree(drv)
	rail := tree.SchemaRail

	// Real leaves do NOT implement dirtyFlusher: lock that, so the container's
	// focus-loss flush loop is provably a no-op for the current leaves. (They
	// also carry only BaseContext's no-op focus hooks; the side-effect-free
	// assertion below proves no override sneaks in.)
	if _, ok := any(tree.Schemas).(dirtyFlusher); ok {
		t.Fatalf("SchemasContext unexpectedly implements dirtyFlusher; " +
			"the container would flush it on focus loss (T2/T3 regression?)")
	}
	if _, ok := any(tree.Tables).(dirtyFlusher); ok {
		t.Fatalf("TablesContext unexpectedly implements dirtyFlusher; " +
			"the container would flush it on focus loss (T2/T3 regression?)")
	}

	drv.lastContent = ""
	drv.lastView = ""
	if err := rail.HandleFocus(types.OnFocusOpts{}); err != nil {
		t.Fatalf("container HandleFocus errored: %v", err)
	}
	if err := rail.HandleFocusLost(types.OnFocusLostOpts{}); err != nil {
		t.Fatalf("container HandleFocusLost errored: %v", err)
	}
	// Neither hook nor flush touches the shared view (the real leaves render
	// only via HandleRender, never via their inherited no-op focus hooks).
	if drv.lastContent != "" || drv.lastView != "" {
		t.Errorf("container focus hooks wrote the shared view (view=%q content=%q), "+
			"want no side effects for hook-less/flush-less leaves", drv.lastView, drv.lastContent)
	}
}

// TestSchemaRail_ContainerFocusLostFlushesNothingForCurrentLeaves proves, with
// counting spies that ALSO lack the dirtyFlusher seam (plain fakeLeaf), that
// the container HandleFocusLost flush loop fires zero flushes when no leaf
// implements dirtyFlusher — and that the active leaf's own HandleFocusLost is
// still delegated exactly once (the container always-delegates). This pins the
// "zero flushes for the current Schemas/Tables leaves" half of item 2 with an
// explicit flush counter, since the real leaves cannot count.
func TestSchemaRail_ContainerFocusLostFlushesNothingForCurrentLeaves(t *testing.T) {
	rail, schemas, tables := newSchemaRailWithSpies(&railTestDriver{})

	if err := rail.HandleFocusLost(types.OnFocusLostOpts{}); err != nil {
		t.Fatalf("container HandleFocusLost errored: %v", err)
	}
	// Active leaf (Schemas) gets its own HandleFocusLost delegated once; the
	// inactive Tables leaf is not flushed (it is not a dirtyFlusher). fakeLeaf
	// is not a dirtyFlusher, so the flush loop is a no-op for both.
	if schemas.focusLostCount != 1 {
		t.Errorf("active Schemas HandleFocusLost fired %d times, want 1 (always delegate)", schemas.focusLostCount)
	}
	if tables.focusCount != 0 || tables.focusLostCount != 0 {
		t.Errorf("inactive Tables hooks fired (f=%d fl=%d), want 0", tables.focusCount, tables.focusLostCount)
	}
}

// TestSchemaRail_ScrollFollowsCursorRestoresOriginAtMostOnce mirrors
// TestSchemaRail_ScrollFollowsCursorAcrossFrames using the deferred-Update
// driver, but focuses on the INCOMING-tab origin restore (coverage item 4): on
// the switch frame the saved origin is re-applied exactly once, and on the
// subsequent frame (no switch) the leaf's cursor-driven scroll is NOT clobbered
// back to the stale per-tab origin.
func TestSchemaRail_ScrollFollowsCursorRestoresOriginAtMostOnce(t *testing.T) {
	v := gocui.NewView("schemas-tables", 0, 0, 20, 10, gocui.OutputNormal)
	drv := &railDeferredDriver{view: v}
	tree := newRailTree(drv)

	// Seed the Tables tab with enough rows that its cursor can scroll.
	rows := make([]any, 30)
	for i := range rows {
		rows[i] = models.Table{Name: fmt.Sprintf("table_%02d", i)}
	}
	tree.Tables.SetItems(rows)
	tree.Tables.SetCursor(29)

	// On Schemas (origin 0,0), pan vertically is irrelevant; switch to Tables.
	// The switch sets restorePending; the switch-frame HandleRender restores
	// Tables' saved origin (0,0) exactly once.
	tree.SchemaRail.SetActiveTab(SchemaRailTabTables)
	if err := tree.SchemaRail.HandleRender(); err != nil {
		t.Fatalf("HandleRender switch frame: %v", err)
	}
	drv.drain() // FocusPoint scrolls oy down to keep row 29 visible
	_, oy1 := v.Origin()
	if oy1 == 0 {
		t.Fatalf("switch frame: oy=0, cursor at row 29 never scrolled into view")
	}

	// Next frame: NO switch. restorePending was consumed on the switch frame,
	// so the synchronous restore at the top of HandleRender must NOT yank oy
	// back to the stale saved origin (0) — the leaf's cursor scroll wins.
	if err := tree.SchemaRail.HandleRender(); err != nil {
		t.Fatalf("HandleRender frame 2: %v", err)
	}
	if _, oy2 := v.Origin(); oy2 != oy1 {
		t.Fatalf("frame 2 restore clobbered cursor scroll: oy=%d, want %d "+
			"(origin restored at most once on the switch frame)", oy2, oy1)
	}
}

// TestSchemaRail_NilLoggerTabSwitchEmitsNoLines locks the "silent for
// SchemaRail" guarantee end-to-end (coverage item 6). SchemaRail never calls
// SetLogger, so the core's log is nil; logTabSwitch is nil-safe via logs.Event.
// A real tab switch must neither panic nor emit any line. There is no capturing
// logger seam on a SchemaRail built via the tree (SetLogger is promoted but
// never invoked by setup.go), so we assert the no-panic + correct-switch
// behavior; the metadata-only/zero-line content is already locked by the core
// test TestTabbedRail_TabSwitchEmitsMetadataOnlyEvent + the nil-logger core
// test TestTabbedRail_NilLoggerSwitchDoesNotPanic.
func TestSchemaRail_NilLoggerTabSwitchEmitsNoLines(t *testing.T) {
	tree := newRailTree(&railTestDriver{})
	rail := tree.SchemaRail

	// No SetLogger called anywhere in the SchemaRail wiring: log is nil.
	rail.SetActiveTab(SchemaRailTabTables) // must not panic
	if rail.ActiveTab() != SchemaRailTabTables {
		t.Errorf("active tab = %d, want %d", rail.ActiveTab(), SchemaRailTabTables)
	}
	rail.SetActiveTab(SchemaRailTabSchemas) // switch back, still silent/no panic
	if rail.ActiveTab() != SchemaRailTabSchemas {
		t.Errorf("active tab = %d after switch-back, want %d", rail.ActiveTab(), SchemaRailTabSchemas)
	}
}

// TestSchemaRail_NoOpSwitchRecordsNoMarkerChangeAndZeroHooks covers item 8: a
// no-op SetActiveTab (idx == active, or an over-range clamp onto the current
// tab) records no extra marker change in the published tab strip and fires zero
// leaf hooks. Spy leaves tolerate the standalone build without panicking.
func TestSchemaRail_NoOpSwitchRecordsNoMarkerChangeAndZeroHooks(t *testing.T) {
	drv := &railTestDriver{}
	rail, schemas, tables := newSchemaRailWithSpies(drv)

	// Establish a baseline marker via one render (Schemas active).
	if err := rail.HandleRender(); err != nil {
		t.Fatalf("HandleRender baseline: %v", err)
	}
	baseline := len(drv.tabsCalls)

	// No-op switch onto the already-active tab fires nothing and changes no
	// marker (no render is triggered by SetActiveTab itself).
	rail.SetActiveTab(SchemaRailTabSchemas)
	if len(drv.tabsCalls) != baseline {
		t.Errorf("no-op switch published %d extra tab strips, want 0", len(drv.tabsCalls)-baseline)
	}
	if schemas.focusCount != 0 || schemas.focusLostCount != 0 ||
		tables.focusCount != 0 || tables.focusLostCount != 0 {
		t.Errorf("no-op switch fired hooks: schemas f=%d fl=%d, tables f=%d fl=%d, want all 0",
			schemas.focusCount, schemas.focusLostCount, tables.focusCount, tables.focusLostCount)
	}
	if rail.ActiveTab() != SchemaRailTabSchemas {
		t.Errorf("active tab = %d after no-op, want %d", rail.ActiveTab(), SchemaRailTabSchemas)
	}
}

// TestSchemaRailContext_OptionsBarFilterScopesInspectToTablesTab locks the
// fix for the leaked status-bar hint: SchemaRailInspect (the `i` "inspect
// table" binding) is tab-unique to Tables, so the options bar must NOT
// advertise it while the Schemas tab is active. Tab-agnostic ShowInBar
// actions stay visible on both tabs.
func TestSchemaRailContext_OptionsBarFilterScopesInspectToTablesTab(t *testing.T) {
	tree := newRailTree(&railTestDriver{})
	rail := tree.SchemaRail
	if rail.ActiveTab() != SchemaRailTabSchemas {
		t.Fatalf("precondition: active tab = %d, want Schemas (%d)", rail.ActiveTab(), SchemaRailTabSchemas)
	}

	schemasFilter := rail.OptionsBarFilter()
	if schemasFilter == nil {
		t.Fatal("Schemas tab OptionsBarFilter() = nil, want a predicate that hides Tables-only actions")
	}
	if schemasFilter(commands.SchemaRailInspect) {
		t.Error("Schemas tab advertises SchemaRailInspect, want it hidden")
	}
	for _, id := range []string{
		commands.SchemaRailConfirm,
		commands.RailTabNext,
		commands.RailTabPrev,
	} {
		if !schemasFilter(id) {
			t.Errorf("Schemas tab hides tab-agnostic %q, want it visible", id)
		}
	}

	rail.SetActiveTab(SchemaRailTabTables)
	tablesFilter := rail.OptionsBarFilter()
	if tablesFilter == nil || !tablesFilter(commands.SchemaRailInspect) {
		t.Error("Tables tab hides SchemaRailInspect, want it visible")
	}
}
