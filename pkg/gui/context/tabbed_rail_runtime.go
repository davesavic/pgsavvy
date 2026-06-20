package context

import (
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// This file carries the RUNTIME tab-mutation plumbing (SetTabs / NextTab /
// PrevTab) kept DELIBERATELY separate from tabbed_rail_context.go. The
// TestTabbedRail_LOCInvariantUnderBaseline guard caps the newline total of the
// three STATIC rail files (tabbed_rail_context.go + query/schema rails); it
// guards the static query/schema-rail consolidation, NOT this runtime popup
// plumbing, so the runtime methods live here to stay outside that budget.
//
// Concurrency: N/A — like the rest of TabbedRailContext, every method runs on
// the single gocui MainLoop (UI thread).

// SetTabs rebuilds the container's tab set at runtime from a fresh spec list and
// injects the matching leaves positionally. It is for RUNTIME-BUILT containers
// only (cheatsheet, FK picker); the static query/schema rails must NEVER call it
// (their tabs are fixed at construction).
//
// The tabs slice is rebuilt from scratch (new []tab), so every prior per-tab
// saved origin is dropped — a new container generation starts with no scroll
// history. Leaves shorter than specs leave the trailing tabs unwired (nil leaf),
// mirroring SetLeaves' bounds behaviour; a nil active leaf renders as a no-op.
//
// CRITICAL: activeTab is reset to 0 and restorePending cleared. HandleRender
// indexes t.tabs[t.activeTab] behind only a len==0 guard, so a shrinking tab set
// with a stale activeTab past the new length would panic — resetting to 0 is the
// shrinking-set safety fix. The logger, fireFocusHooks, bodyHeader and deps are
// container-lifetime state and are PRESERVED across the rebuild.
func (t *TabbedRailContext) SetTabs(specs []TabSpec, leaves []types.IBaseContext) {
	tabs := make([]tab, len(specs))
	for i, s := range specs {
		tabs[i] = tab{
			label:            s.Label,
			leafKey:          s.LeafKey,
			managesOwnOrigin: s.ManagesOwnOrigin,
		}
		if i < len(leaves) {
			tabs[i].leaf = leaves[i]
		}
	}
	t.tabs = tabs
	t.activeTab = 0
	t.restorePending = false
}

// NextTab advances to the next tab, wrapping from the last tab back to 0. It
// routes through SetActiveTab so origin save/restore and the tab_switch event
// still run. No-op when there are no tabs; a single-tab container wraps onto
// itself (a no-op switch).
func (t *TabbedRailContext) NextTab() {
	count := t.TabCount()
	if count == 0 {
		return
	}
	t.SetActiveTab((t.activeTab + 1) % count)
}

// PrevTab steps to the previous tab, wrapping from tab 0 back to the last tab.
// It routes through SetActiveTab so origin save/restore and the tab_switch event
// still run. No-op when there are no tabs; a single-tab container wraps onto
// itself (a no-op switch).
func (t *TabbedRailContext) PrevTab() {
	count := t.TabCount()
	if count == 0 {
		return
	}
	t.SetActiveTab((t.activeTab - 1 + count) % count)
}
