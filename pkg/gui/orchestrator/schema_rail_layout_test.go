package orchestrator_test

import (
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/theme"
)

// themeFrameAttr mirrors orchestrator.frameAttr (unexported) so this external
// test can compute the expected gocui.Attribute the colour pass applies.
func themeFrameAttr(s *theme.Style) gocui.Attribute {
	if s == nil || s.Fg == "" {
		return gocui.ColorDefault
	}
	return gocui.GetColor(s.Fg)
}

// TestSchemaRailConsolidatedLayout asserts the many-contexts-ONE-view
// topology end-to-end at the layout level: the SCHEMA_RAIL container is
// the sole renderer of the single "schemas-tables" view, and the retired
// per-rail views "schemas"/"tables" are never SetView'd.
func TestSchemaRailConsolidatedLayout(t *testing.T) {
	g, rec := buildTestGui(t)
	// Push the container so the Tier-1 side-rail loop paints it (the
	// CONNECTION_MANAGER modal otherwise suppresses Tier-1).
	if err := g.ContextTree().Push(g.Registry().SchemaRail); err != nil {
		t.Fatalf("Push(SchemaRail): %v", err)
	}
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}

	// Exactly one SetView for the shared rail view.
	got := 0
	for _, c := range rec.AllSetViewCalls() {
		if c.Name == "schemas-tables" {
			got++
		}
	}
	if got != 1 {
		t.Errorf("SetView(schemas-tables) count = %d, want exactly 1", got)
	}
	// None for the retired per-rail views.
	for _, name := range []string{string(types.SCHEMAS), string(types.TABLES)} {
		if rec.HasSetView(name) {
			t.Errorf("retired rail view %q was SetView'd; the container owns schemas-tables", name)
		}
	}
}

// TestSchemaRailTabMarkerAndColorsApplied asserts the .6 visual seam after a
// real layout frame: the published tab strip carries the active-tab MARKER
// ("[Schemas]" while Schemas is active) and the orchestrator applies the
// native active/inactive tab colours to the container view each frame.
func TestSchemaRailTabMarkerAndColorsApplied(t *testing.T) {
	g, rec := buildTestGui(t)
	if err := g.ContextTree().Push(g.Registry().SchemaRail); err != nil {
		t.Fatalf("Push(SchemaRail): %v", err)
	}
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}

	// Active-tab marker baked into the label slice for "schemas-tables".
	var lastTabs *testfake.SetViewTabsCall
	for i := range rec.AllSetViewTabsCalls() {
		c := rec.AllSetViewTabsCalls()[i]
		if c.Name == context.SchemaRailViewName {
			lastTabs = &c
		}
	}
	if lastTabs == nil {
		t.Fatal("no SetViewTabs call for schemas-tables")
	}
	if len(lastTabs.Labels) != 2 || lastTabs.Labels[0] != "[Schemas]" || lastTabs.Labels[1] != "Tables" {
		t.Errorf("tab labels = %v, want [[Schemas] Tables] (marker on active Schemas)", lastTabs.Labels)
	}

	// Native tab colours applied to the container view this frame: the active
	// tab gets ActiveBorder via SelFgColor. The inactive colour goes to the
	// view's FgColor, which gocui ALSO uses as the default foreground for the
	// view's content (view.go: plain cells fall back to v.FgColor). A dim
	// border colour there greys every list row, so the inactive colour must be
	// ColorDefault — content renders at the terminal foreground and the active
	// tab stays distinguished by SelFgColor + its "[...]" bracket marker.
	wantActive := themeFrameAttr(theme.Current().ActiveBorder)
	wantInactive := gocui.ColorDefault
	var lastColors *testfake.SetViewTabColorsCall
	for i := range rec.AllSetViewTabColorsCalls() {
		c := rec.AllSetViewTabColorsCalls()[i]
		if c.Name == context.SchemaRailViewName {
			lastColors = &c
		}
	}
	if lastColors == nil {
		t.Fatal("no SetViewTabColors call for schemas-tables")
	}
	if lastColors.ActiveFg != wantActive || lastColors.InactiveFg != wantInactive {
		t.Errorf("tab colours = (active %v, inactive %v), want (active %v, inactive %v)",
			lastColors.ActiveFg, lastColors.InactiveFg, wantActive, wantInactive)
	}
}

// TestSchemaRailTabClickSetsActiveIndex drives the native tab-click binding via
// the recorder's FeedTabClick fire hook and asserts the click switches the
// container's active tab (the right-most index selects Tables — no off-by-one).
func TestSchemaRailTabClickSetsActiveIndex(t *testing.T) {
	g, rec := buildTestGui(t)

	// Default is Schemas; click the right-most tab (index 1 = Tables).
	if got := g.Registry().SchemaRail.ActiveTab(); got != context.SchemaRailTabSchemas {
		t.Fatalf("pre-click active tab = %d, want Schemas", got)
	}
	if err := rec.FeedTabClick(context.SchemaRailViewName, context.SchemaRailTabTables); err != nil {
		t.Fatalf("FeedTabClick(Tables): %v", err)
	}
	if got := g.Registry().SchemaRail.ActiveTab(); got != context.SchemaRailTabTables {
		t.Errorf("post-click active tab = %d, want Tables (%d)", got, context.SchemaRailTabTables)
	}

	// Click back to the left-most tab (index 0 = Schemas).
	if err := rec.FeedTabClick(context.SchemaRailViewName, context.SchemaRailTabSchemas); err != nil {
		t.Fatalf("FeedTabClick(Schemas): %v", err)
	}
	if got := g.Registry().SchemaRail.ActiveTab(); got != context.SchemaRailTabSchemas {
		t.Errorf("post-click active tab = %d, want Schemas (%d)", got, context.SchemaRailTabSchemas)
	}
}

// TestSchemaRailFlattenTopology asserts the ContextTree wiring: the
// container is flattened, the leaves are not, and no flattened context
// other than the container renders the shared view.
func TestSchemaRailFlattenTopology(t *testing.T) {
	tree := context.NewContextTree(types.ContextTreeDeps{})

	var sawRail bool
	for _, c := range tree.Flatten() {
		switch c.GetKey() {
		case types.SCHEMA_RAIL:
			sawRail = true
		case types.SCHEMAS, types.TABLES:
			t.Errorf("leaf %s must NOT be in Flatten() (inFlatten=false)", c.GetKey())
		}
		// Only the container may own the shared view among flattened contexts.
		if c.GetViewName() == "schemas-tables" && c.GetKey() != types.SCHEMA_RAIL {
			t.Errorf("flattened context %s also renders schemas-tables; only SCHEMA_RAIL may", c.GetKey())
		}
	}
	if !sawRail {
		t.Error("SCHEMA_RAIL container missing from Flatten()")
	}

	// The leaves remain reachable via named fields and resolve to the shared
	// view (so HandleRender writes it when the container calls them).
	for _, leaf := range []types.IBaseContext{tree.Schemas, tree.Tables} {
		if leaf.GetViewName() != "schemas-tables" {
			t.Errorf("%s.GetViewName() = %q, want schemas-tables", leaf.GetKey(), leaf.GetViewName())
		}
	}
}

// TestAllContextKeysIncludesSchemaRail guards the scope:all binding seam:
// SCHEMA_RAIL must be enumerated in AllContextKeys() so globals (the
// command-line ':' and other scope:all bindings) resolve while the rail is
// focused.
func TestAllContextKeysIncludesSchemaRail(t *testing.T) {
	found := false
	for _, k := range types.AllContextKeys() {
		if k == types.SCHEMA_RAIL {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("AllContextKeys() missing SCHEMA_RAIL; scope:all bindings die when the rail is focused")
	}
}

// TestSchemaRailMasterEditorAttachedByViewName proves the dispatch wiring:
// the SCHEMA_RAIL master editor exists and is attached to the shared view
// "schemas-tables" by the Tier-1 layout pass (its scope token "schema-rail"
// differs from its view name).
func TestSchemaRailMasterEditorAttachedByViewName(t *testing.T) {
	g, rec := buildTestGui(t)
	if err := g.ContextTree().Push(g.Registry().SchemaRail); err != nil {
		t.Fatalf("Push(SchemaRail): %v", err)
	}
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	editors := rec.InstalledEditors()
	if _, ok := editors["schemas-tables"]; !ok {
		keys := make([]string, 0, len(editors))
		for k := range editors {
			keys = append(keys, k)
		}
		t.Errorf("no master editor attached to view schemas-tables; got views %v", keys)
	}
}

// fakeTablePicker is a no-op SelectedTable accessor for the double-click test.
type fakeTablePicker struct{}

func (fakeTablePicker) SelectedTable() *models.Table { return nil }

// TestSchemaRailDoubleClickGatedToContainerView is a regression test for the
// .6 mouse re-home: the former TABLES double-click stub is homed on the
// consolidated "schemas-tables" container view and must fire ONLY there.
// WireMouse registers the left-click bundle (incl. the double-click branch)
// on EVERY non-stub flattened view, so without the container-view gate a
// double-click in the query editor would fire the table stub whenever the
// rail's active tab is Tables. With Tables active we assert: double-click on
// the query-editor view is a NO-OP, while double-click on "schemas-tables"
// fires the stub.
func TestSchemaRailDoubleClickGatedToContainerView(t *testing.T) {
	// A clean registry (not buildTestGui) so the ONLY recorded mouse
	// bindings are the ones this test's WireMouse installs — buildTestGui
	// already carries production bindings whose handlers target a toast we
	// cannot inspect.
	registry := context.NewContextTree(types.ContextTreeDeps{})
	registry.SchemaRail.SetActiveTab(context.SchemaRailTabTables)
	rec := testfake.NewRecorderGuiDriver()

	toast := ui.NewToastHelper(nil)
	tr := i18n.EnglishTranslationSet()
	th := ui.NewTablesHelper(toast, tr)

	// Tree: nil isolates the double-click logic from the focus stack
	// (pushFocus skips tree.Push when Tree is nil).
	if err := ui.WireMouse(ui.MouseWiringDeps{
		Driver:      rec,
		Registry:    registry,
		TableDouble: th,
		TablePicker: fakeTablePicker{},
		RailActiveTabIsTables: func() bool {
			return registry.SchemaRail.ActiveTab() == context.SchemaRailTabTables
		},
	}); err != nil {
		t.Fatalf("WireMouse: %v", err)
	}

	dbl := types.ViewMouseBindingOpts{IsDoubleClick: true}

	// Double-click in the query editor must NOT fire the table stub.
	if err := rec.FeedMouse(string(types.QUERY_EDITOR), types.MouseLeft, types.ModNone, dbl); err != nil {
		t.Fatalf("FeedMouse(query_editor): %v", err)
	}
	if got := toast.Current(); got != "" {
		t.Fatalf("double-click in query editor fired the table stub (toast=%q); want no-op", got)
	}

	// Double-click on the consolidated rail with Tables active MUST fire it.
	if err := rec.FeedMouse(context.SchemaRailViewName, types.MouseLeft, types.ModNone, dbl); err != nil {
		t.Fatalf("FeedMouse(schemas-tables): %v", err)
	}
	if got := toast.Current(); got != tr.TableDataEditDeferred {
		t.Fatalf("double-click on rail Tables tab: toast=%q; want %q", got, tr.TableDataEditDeferred)
	}
}
