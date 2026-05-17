package types

import "github.com/davesavic/dbsavvy/pkg/common"

// IGuiCommon is the minimum aggregator surface every gui-layer collaborator
// receives. It exposes the shared cross-cutting bag declared in
// pkg/common.Common; richer accessors are layered on by HelperCommon and
// by epic-specific extensions.
type IGuiCommon interface {
	Common() *common.Common
}

// HelperCommon is the aggregator passed to controllers, helpers, and
// contexts. It embeds IGuiCommon today; later epics extend it with
// accessors for the Helpers bag, presentation, and GuiDriver.
type HelperCommon interface {
	IGuiCommon
}

// ContextTreeDeps is the injection bag NewContextTree consumes in T2
// (concrete contexts). It carries hooks for empty-state predicates,
// presentation callbacks, and i18n lookups that the concrete Context
// implementations need but which the stack semantics in T1 do not.
// Empty today; populated by T2.
type ContextTreeDeps struct{}
