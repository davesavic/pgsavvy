package context

import (
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// FKReversePickerContextKey aliases types.FK_REVERSE_PICKER. The
// canonical ContextKey was promoted into
// pkg/gui/types/context.go; this alias is retained so existing callers
// (controllers, tests) keep compiling without a wider rename. New code
// should reference types.FK_REVERSE_PICKER directly.
const FKReversePickerContextKey = types.FK_REVERSE_PICKER

// FKReversePickerContext renders the gD reverse FK picker as a
// many-contexts-ONE-view tabbed pane (one tab per inbound FK). It is a
// THIN ADAPTER over the shared TabbedRailContext core: all tabbed-pane
// mechanics (tab switching, tab-strip publishing, leaf-delegation render)
// live in the embedded core; the controller builds one DisplayLeafContext
// per inbound FK and injects them via SetTabs each time the picker opens.
//
// TEMPORARY_POPUP kind — the orchestrator's layout pass gates rendering on
// focus-stack membership, so HandleRender (provided by the embedded core)
// is only invoked while the popup is on top. The body is ~2 lines, so the
// adapter adds NO scroll (unlike CheatsheetContext).
type FKReversePickerContext struct {
	*TabbedRailContext

	deps Deps
}

// NewFKReversePickerContext builds the FK_REVERSE_PICKER container as a thin
// adapter over a TabbedRailContext core with NO initial tabs (the per-FK
// tabs are built and injected at runtime via SetTabs each time the picker
// opens). The caller sets Key / ViewName / Kind via BaseContextOpts.
func NewFKReversePickerContext(base BaseContext, deps Deps) *FKReversePickerContext {
	core := NewTabbedRailContext(base, deps, TabbedRailOpts{
		// FireFocusHooks=false: every leaf shares the single
		// FK_REVERSE_PICKER scope, so a tab switch is NOT a focus
		// transition — firing per-leaf focus hooks would be spurious.
		// The leaves are stateless body renderers.
		FireFocusHooks: false,
	})
	return &FKReversePickerContext{
		TabbedRailContext: core,
		deps:              deps,
	}
}
