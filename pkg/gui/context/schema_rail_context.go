package context

import (
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// SchemaRailTabSchemas / SchemaRailTabTables are the fixed active-tab
// indices the SCHEMA_RAIL container multiplexes. The default tab is
// Schemas (0). Exported so the connection-open focus push (orchestrator)
// can select the initial tab.
const (
	SchemaRailTabSchemas = 0
	SchemaRailTabTables  = 1
)

// SchemaRailViewName is the single gocui view the SCHEMA_RAIL container and
// both leaves render into (many-contexts-ONE-view topology). Exported so the
// orchestrator can target tab colours / the tab-click binding without
// duplicating the literal.
const SchemaRailViewName = "schemas-tables"

// schemaRailTabNames are the plain (markerless) tab labels. The active tab
// carries a marker baked into its label so it stays distinguishable when the
// rail is unfocused (gocui's drawTitle only de-highlights the active tab on
// the focused view) and under NO_COLOR / mono terminals where the native tab
// colours are not visible. The marker brackets the active label.
var schemaRailTabNames = [2]string{"Schemas", "Tables"}

// schemaRailTabLabelsByActive holds the per-active-tab label slices, hoisted
// so HandleRender publishes the strip every frame without re-allocating.
// Index = active tab; the slice at that index has the active label bracketed.
var schemaRailTabLabelsByActive = [2][]string{
	SchemaRailTabSchemas: {markActiveTab(schemaRailTabNames[0]), schemaRailTabNames[1]},
	SchemaRailTabTables:  {schemaRailTabNames[0], markActiveTab(schemaRailTabNames[1])},
}

// markActiveTab brackets a label so the active tab reads e.g. "[Schemas]".
// Brackets work in NO_COLOR / mono and regardless of focus.
func markActiveTab(label string) string { return "[" + label + "]" }

// SchemaRailContext is the SIDE_CONTEXT container that multiplexes the
// SchemasContext and TablesContext leaves into the single "schemas-tables"
// view (many-contexts-ONE-view topology). The container is the ONLY
// flattened side context for the consolidated rail and the ONLY renderer
// of view "schemas-tables"; the two leaves carry inFlatten=false and their
// GetViewName() resolves to "schemas-tables", so HandleRender writes the
// shared view — but ONLY when the container calls the active leaf directly.
// The leaves are never pushed onto the focus stack; switching tabs mutates
// activeTab and re-renders.
//
// Per-tab scroll origin is stored here because SideListContext has only a
// cursor (no origin field): on a tab switch the outgoing tab's view origin
// is captured and the incoming tab's saved origin is restored, so a
// horizontal pan on one tab does not bleed into the other through the
// shared view.
type SchemaRailContext struct {
	BaseContext

	deps Deps

	schemas *SchemasContext
	tables  *TablesContext

	activeTab int
	// origins holds the saved (ox, oy) view origin per tab index. Index 0
	// is Schemas, index 1 is Tables.
	origins [2][2]int
	// restorePending is set by SetActiveTab and consumed by the next
	// HandleRender so the incoming tab's saved origin is re-applied exactly
	// ONCE, on the switch frame. Restoring every frame would clobber the
	// leaf's vertical scroll-to-cursor (FocusPoint runs in the deferred Update
	// queue, after the synchronous restore) and the horizontal pan handlers,
	// pinning the view to the stale per-tab origin so it never follows the
	// cursor.
	restorePending bool
}

// NewSchemaRailContext builds the container bound to the SCHEMA_RAIL key
// and the shared "schemas-tables" view. The leaf references are injected
// by setup.go after all three contexts are constructed (the spec build
// order constructs the leaves before the container).
func NewSchemaRailContext(base BaseContext, deps Deps) *SchemaRailContext {
	return &SchemaRailContext{
		BaseContext: base,
		deps:        deps,
		activeTab:   SchemaRailTabSchemas,
	}
}

// SetLeaves injects the live leaf contexts. Called once at wiring time.
func (s *SchemaRailContext) SetLeaves(schemas *SchemasContext, tables *TablesContext) {
	s.schemas = schemas
	s.tables = tables
}

// ActiveTab returns the current active-tab index (0=Schemas, 1=Tables).
func (s *SchemaRailContext) ActiveTab() int { return s.activeTab }

// SetActiveTab switches the active tab, clamping idx into range. Switching
// captures the outgoing tab's view origin so a later switch back restores
// it. A no-op switch (idx == activeTab) leaves the saved origins untouched.
// Exported for .6's tab-click handler and .5's '['/']' cycle commands.
func (s *SchemaRailContext) SetActiveTab(idx int) {
	idx = clampSchemaRailTab(idx)
	if idx == s.activeTab {
		return
	}
	s.saveActiveOrigin()
	s.activeTab = idx
	s.restorePending = true
}

// ActiveLeaf returns the currently active leaf's SideListContext, or nil
// when the leaves are not yet wired. railForScope uses this so rail search
// targets the visible tab.
func (s *SchemaRailContext) ActiveLeaf() *SideListContext {
	leaf := s.activeLeafContext()
	if leaf == nil {
		return nil
	}
	return leaf
}

// HandleRender publishes the tab strip and renders ONLY the active leaf
// into the shared view. SetViewTabs is called every frame so gocui's
// drawTitle keeps preferring the live Tabs (with the current active index)
// over the stale v.Title the Tier-1 layout loop sets. The inactive leaf's
// HandleRender is never invoked, so it never writes/scrolls the shared
// view.
func (s *SchemaRailContext) HandleRender() error {
	if s.deps.GuiDriver != nil {
		_ = s.deps.GuiDriver.SetViewTabs(s.GetViewName(), schemaRailTabLabelsByActive[s.activeTab], s.activeTab)
	}
	if s.restorePending {
		s.restoreActiveOrigin()
		s.restorePending = false
	}
	leaf := s.activeLeafContext()
	if leaf == nil {
		return nil
	}
	return s.activeLeaf().HandleRender()
}

// activeLeaf returns the active leaf as its concrete renderer. The two
// concrete types share HandleRender via their embedded SideListContext but
// each overrides it, so render must dispatch on the concrete type, not the
// embedded SideListContext.
func (s *SchemaRailContext) activeLeaf() types.IBaseContext {
	if s.activeTab == SchemaRailTabTables {
		return s.tables
	}
	return s.schemas
}

// activeLeafContext returns the active leaf's SideListContext, or nil when
// the leaf is unwired.
func (s *SchemaRailContext) activeLeafContext() *SideListContext {
	if s.activeTab == SchemaRailTabTables {
		if s.tables == nil {
			return nil
		}
		return &s.tables.SideListContext
	}
	if s.schemas == nil {
		return nil
	}
	return &s.schemas.SideListContext
}

// saveActiveOrigin captures the shared view's current origin into the
// active tab's slot. No-op without a driver or a realised view.
func (s *SchemaRailContext) saveActiveOrigin() {
	if s.deps.GuiDriver == nil {
		return
	}
	v, err := s.deps.GuiDriver.ViewByName(s.GetViewName())
	if err != nil || v == nil {
		return
	}
	ox, oy := v.Origin()
	s.origins[s.activeTab] = [2]int{ox, oy}
}

// restoreActiveOrigin re-applies the active tab's saved origin to the
// shared view. Called ONLY on the switch frame (guarded by restorePending),
// never every frame: it restores the incoming tab's horizontal pan (ox), and
// the leaf's deferred FocusPoint then drives oy from the leaf's own cursor on
// the same frame. Restoring every frame would clobber that cursor-driven oy
// (and any live pan) back to the stale saved origin. No-op without a driver or
// view.
func (s *SchemaRailContext) restoreActiveOrigin() {
	if s.deps.GuiDriver == nil {
		return
	}
	saved := s.origins[s.activeTab]
	v, err := s.deps.GuiDriver.ViewByName(s.GetViewName())
	if err != nil || v == nil {
		return
	}
	v.SetOrigin(saved[0], saved[1])
}

// OptionsBarFilter returns a predicate that hides tab-unique ShowInBar
// actions when their owning tab is not active, so the status bar advertises
// only the active tab's hints rather than the union of all tabs. The
// SCHEMA_RAIL scope registers every tab's bindings under one scope, so
// without this filter SchemaRailInspect (the Tables-only `i` "inspect table"
// binding) leaks onto the Schemas tab. Inspect is currently the only
// tab-unique binding flagged ShowInBar; all other shown bindings are
// tab-agnostic and pass through. The status bar renderer type-asserts to
// this method each frame.
func (s *SchemaRailContext) OptionsBarFilter() func(string) bool {
	onTables := s.activeTab == SchemaRailTabTables
	return func(id string) bool {
		if id == commands.SchemaRailInspect {
			return onTables
		}
		return true
	}
}

// clampSchemaRailTab clamps an arbitrary index into the valid tab range.
func clampSchemaRailTab(idx int) int {
	if idx < SchemaRailTabSchemas {
		return SchemaRailTabSchemas
	}
	if idx > SchemaRailTabTables {
		return SchemaRailTabTables
	}
	return idx
}
