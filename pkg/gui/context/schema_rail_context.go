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

// SchemaRailContext is the SIDE_CONTEXT container that multiplexes the
// SchemasContext and TablesContext leaves into the single "schemas-tables"
// view (many-contexts-ONE-view topology). It is a THIN ADAPTER over the shared
// TabbedRailContext core: all tabbed-pane mechanics (tab switching, per-tab
// origin save/restore, tab-strip publishing) live in the embedded core; this
// type exists only so the registry can hold a stable *SchemaRailContext and so
// the SchemaRail-specific surface (ActiveLeaf, OptionsBarFilter) keeps its
// shape.
//
// The SCHEMA_RAIL constructs the core with FireFocusHooks=FALSE: both leaves
// (schemas, tables) share ONE SCHEMA_RAIL master-editor scope, so a tab switch
// is NOT a focus transition — firing per-leaf focus hooks would be spurious.
type SchemaRailContext struct {
	*TabbedRailContext
}

// NewSchemaRailContext builds the container as a thin adapter over a
// TabbedRailContext core constructed with FireFocusHooks=FALSE. The two tab
// specs (Schemas, Tables) are injected internally. The active tab defaults to
// Schemas (0). Leaf references are injected via the promoted SetLeaves after
// all three contexts are constructed (the spec build order constructs the
// leaves before the container).
func NewSchemaRailContext(base BaseContext, deps Deps) *SchemaRailContext {
	core := NewTabbedRailContext(base, deps, TabbedRailOpts{FireFocusHooks: false},
		TabSpec{Label: "Schemas", LeafKey: types.SCHEMAS, ManagesOwnOrigin: false},
		TabSpec{Label: "Tables", LeafKey: types.TABLES, ManagesOwnOrigin: false},
	)
	return &SchemaRailContext{TabbedRailContext: core}
}

// ActiveLeaf returns the currently active leaf's SideListContext, or nil when
// the leaves are not yet wired. railForScope uses this so rail search targets
// the visible tab. The returned pointer is the leaf's IDENTICAL embedded
// SideListContext (never a copy), so callers mutating cursor/origin act on the
// live leaf.
func (s *SchemaRailContext) ActiveLeaf() *SideListContext {
	switch leaf := s.activeLeaf().(type) {
	case *SchemasContext:
		return &leaf.SideListContext
	case *TablesContext:
		return &leaf.SideListContext
	default:
		return nil
	}
}

// OptionsBarFilter returns a predicate that hides tab-unique ShowInBar
// actions when their owning tab is not active, so the status bar advertises
// only the active tab's hints rather than the union of all tabs. The
// SCHEMA_RAIL scope registers every tab's bindings under one scope, so
// without this filter SchemaRailInspect (the Tables-only `i` "inspect table"
// binding) leaks onto the Schemas tab. Inspect is currently the only
// tab-unique binding flagged ShowInBar; all other shown bindings are
// tab-agnostic and pass through. The status bar renderer type-asserts to
// this method each frame. The core deliberately does NOT define it.
func (s *SchemaRailContext) OptionsBarFilter() func(string) bool {
	onTables := s.ActiveTab() == SchemaRailTabTables
	return func(id string) bool {
		if id == commands.SchemaRailInspect {
			return onTables
		}
		return true
	}
}

// OptionsBarScope tells the status-bar renderer to COLLECT this container's
// hints from the SCHEMA_RAIL dispatch scope rather than from the active leaf
// key (SCHEMAS/TABLES). The schema rail registers every tab's bindings under
// the single SCHEMA_RAIL scope (key dispatch stays there — see
// schema_rail_controller.go GetKeybindings), so the per-leaf scopes carry no
// ShowInBar bindings; redirecting the options scope to them would blank the
// bar. Per-tab visibility (Inspect on the Tables tab only) is handled by
// OptionsBarFilter above. Mode lookup still uses the leaf key (always Normal
// for these list tabs, so unaffected). The QUERY_RAIL does NOT implement this
// method, so its per-leaf options scope is unchanged.
func (s *SchemaRailContext) OptionsBarScope() types.ContextKey {
	return types.SCHEMA_RAIL
}
