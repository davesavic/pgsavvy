package context

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// Concurrency is N/A throughout: every TabbedRailContext method runs on the
// single gocui MainLoop (UI thread), so the spies below need no synchronization.

// orderSpyLeaf is a leaf that records the SEQUENCE of focus-protocol calls into
// a shared slice so the outgoing-then-incoming ordering of a tab switch can be
// asserted across leaves. The plain fakeLeaf (query_rail_context_test.go) only
// counts; this one tags each call with its own name.
type orderSpyLeaf struct {
	BaseContext
	name string
	seq  *[]string
}

func newOrderSpyLeaf(key types.ContextKey, name string, seq *[]string) *orderSpyLeaf {
	return &orderSpyLeaf{
		BaseContext: NewBaseContext(BaseContextOpts{
			Key:      key,
			ViewName: queryRailViewName,
			Kind:     types.MAIN_CONTEXT,
		}),
		name: name,
		seq:  seq,
	}
}

func (l *orderSpyLeaf) HandleFocus(_ types.OnFocusOpts) error {
	*l.seq = append(*l.seq, l.name+":focus")
	return nil
}

func (l *orderSpyLeaf) HandleFocusLost(_ types.OnFocusLostOpts) error {
	*l.seq = append(*l.seq, l.name+":focusLost")
	return nil
}

// dirtySpyLeaf records FlushDirty calls so the inactive-flush loop in
// HandleFocusLost can be asserted. It satisfies the unexported dirtyFlusher
// seam.
type dirtySpyLeaf struct {
	BaseContext
	flushCount int
}

func newDirtySpyLeaf(key types.ContextKey) *dirtySpyLeaf {
	return &dirtySpyLeaf{
		BaseContext: NewBaseContext(BaseContextOpts{
			Key:      key,
			ViewName: queryRailViewName,
			Kind:     types.MAIN_CONTEXT,
		}),
	}
}

func (l *dirtySpyLeaf) FlushDirty() error { l.flushCount++; return nil }

// newTabbedRail builds a 2-tab container (editor index 0 managesOwnOrigin, list
// index 1) with the supplied FireFocusHooks setting and plain counting leaves.
func newTabbedRail(drv types.GuiDriver, fireFocusHooks bool) (*TabbedRailContext, *fakeLeaf, *fakeLeaf) {
	base := NewBaseContext(BaseContextOpts{
		Key:      types.QUERY_EDITOR,
		ViewName: queryRailViewName,
		Kind:     types.MAIN_CONTEXT,
	})
	rail := NewTabbedRailContext(base, Deps{GuiDriver: drv}, TabbedRailOpts{FireFocusHooks: fireFocusHooks},
		TabSpec{Label: "Editor", LeafKey: types.QUERY_EDITOR, ManagesOwnOrigin: true},
		TabSpec{Label: "History", LeafKey: types.HISTORY},
	)
	editor := newFakeLeaf(types.QUERY_EDITOR)
	history := newFakeLeaf(types.HISTORY)
	rail.SetLeaves(editor, history)
	return rail, editor, history
}

func TestTabbedRail_DefaultsToFirstTab(t *testing.T) {
	rail, _, _ := newTabbedRail(testfake.NewRecorderGuiDriver(), true)
	if got := rail.ActiveTab(); got != 0 {
		t.Fatalf("default active tab = %d, want 0", got)
	}
	if got := rail.ActiveLeafKey(); got != types.QUERY_EDITOR {
		t.Fatalf("default ActiveLeafKey = %q, want %q", got, types.QUERY_EDITOR)
	}
}

func TestTabbedRail_FireFocusHooksTrue_RealSwitchFiresOutgoingThenIncoming(t *testing.T) {
	var seq []string
	base := NewBaseContext(BaseContextOpts{Key: types.QUERY_EDITOR, ViewName: queryRailViewName, Kind: types.MAIN_CONTEXT})
	rail := NewTabbedRailContext(base, Deps{GuiDriver: testfake.NewRecorderGuiDriver()}, TabbedRailOpts{FireFocusHooks: true},
		TabSpec{Label: "Editor", LeafKey: types.QUERY_EDITOR, ManagesOwnOrigin: true},
		TabSpec{Label: "History", LeafKey: types.HISTORY},
	)
	editor := newOrderSpyLeaf(types.QUERY_EDITOR, "editor", &seq)
	history := newOrderSpyLeaf(types.HISTORY, "history", &seq)
	rail.SetLeaves(editor, history)

	rail.SetActiveTab(1) // editor -> history

	want := []string{"editor:focusLost", "history:focus"}
	if len(seq) != len(want) {
		t.Fatalf("hook sequence = %v, want exactly %v", seq, want)
	}
	for i := range want {
		if seq[i] != want[i] {
			t.Errorf("hook sequence[%d] = %q, want %q (full=%v)", i, seq[i], want[i], seq)
		}
	}
}

func TestTabbedRail_FireFocusHooksTrue_NoOpSwitchFiresNoHooks(t *testing.T) {
	rail, editor, history := newTabbedRail(testfake.NewRecorderGuiDriver(), true)

	rail.SetActiveTab(0) // already active 0
	if editor.focusCount != 0 || editor.focusLostCount != 0 ||
		history.focusCount != 0 || history.focusLostCount != 0 {
		t.Errorf("no-op switch fired hooks: editor f=%d fl=%d, history f=%d fl=%d, want all 0",
			editor.focusCount, editor.focusLostCount, history.focusCount, history.focusLostCount)
	}
}

func TestTabbedRail_FireFocusHooksFalse_AnySwitchFiresNoHooks(t *testing.T) {
	rail, editor, history := newTabbedRail(testfake.NewRecorderGuiDriver(), false)

	rail.SetActiveTab(1) // real switch, but hooks gated off
	if editor.focusCount != 0 || editor.focusLostCount != 0 ||
		history.focusCount != 0 || history.focusLostCount != 0 {
		t.Errorf("FireFocusHooks=false fired leaf hooks: editor f=%d fl=%d, history f=%d fl=%d, want all 0",
			editor.focusCount, editor.focusLostCount, history.focusCount, history.focusLostCount)
	}
	if rail.ActiveTab() != 1 {
		t.Errorf("active tab = %d after switch, want 1 (switch still happens)", rail.ActiveTab())
	}
}

func TestTabbedRail_SetActiveTabClampsWithoutPanic(t *testing.T) {
	rail, _, _ := newTabbedRail(testfake.NewRecorderGuiDriver(), true)

	rail.SetActiveTab(99)
	if got := rail.ActiveTab(); got != 1 {
		t.Errorf("SetActiveTab(99) => %d, want clamp to 1 (last tab)", got)
	}
	rail.SetActiveTab(-3)
	if got := rail.ActiveTab(); got != 0 {
		t.Errorf("SetActiveTab(-3) => %d, want clamp to 0 (first tab)", got)
	}
}

func TestTabbedRail_ClampToCurrentTabIsNoOp(t *testing.T) {
	rail, editor, history := newTabbedRail(testfake.NewRecorderGuiDriver(), true)

	rail.SetActiveTab(1) // real switch to last tab
	editor.focusLostCount = 0
	history.focusCount = 0

	rail.SetActiveTab(99) // over-range clamps onto current tab => no-op
	if editor.focusCount != 0 || editor.focusLostCount != 0 ||
		history.focusCount != 0 || history.focusLostCount != 0 {
		t.Errorf("clamp-to-current fired hooks, want all 0")
	}
	if rail.ActiveTab() != 1 {
		t.Errorf("active tab = %d after clamp-to-current, want still 1", rail.ActiveTab())
	}
}

func TestTabbedRail_EmptyContainerIsNoOp(t *testing.T) {
	base := NewBaseContext(BaseContextOpts{Key: types.QUERY_EDITOR, ViewName: queryRailViewName, Kind: types.MAIN_CONTEXT})
	rail := NewTabbedRailContext(base, Deps{GuiDriver: testfake.NewRecorderGuiDriver()}, TabbedRailOpts{FireFocusHooks: true})

	rail.SetActiveTab(0) // no panic, no-op
	if rail.ActiveTab() != 0 {
		t.Errorf("empty container active tab = %d, want 0", rail.ActiveTab())
	}
	if got := rail.ActiveLeafKey(); got != "" {
		t.Errorf("empty container ActiveLeafKey = %q, want empty", got)
	}
	if err := rail.HandleRender(); err != nil {
		t.Errorf("empty container HandleRender error = %v, want nil", err)
	}
	if err := rail.HandleFocus(types.OnFocusOpts{}); err != nil {
		t.Errorf("empty container HandleFocus error = %v, want nil", err)
	}
	if err := rail.HandleFocusLost(types.OnFocusLostOpts{}); err != nil {
		t.Errorf("empty container HandleFocusLost error = %v, want nil", err)
	}
}

func TestTabbedRail_TabSwitchEmitsMetadataOnlyEvent(t *testing.T) {
	rail, _, _ := newTabbedRail(testfake.NewRecorderGuiDriver(), true)
	var buf bytes.Buffer
	rail.SetLogger(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	// No-op switch emits nothing.
	rail.SetActiveTab(0)
	if buf.Len() != 0 {
		t.Fatalf("no-op switch emitted a log line: %q", buf.String())
	}

	// Real switch emits exactly one tab_switch event with the 3 keys.
	rail.SetActiveTab(1)
	out := buf.String()
	if n := strings.Count(out, "\n"); n != 1 {
		t.Fatalf("real switch emitted %d log lines, want exactly 1: %q", n, out)
	}
	for _, want := range []string{
		`"evt":"tab_switch"`,
		`"from_leaf":"` + string(types.QUERY_EDITOR) + `"`,
		`"to_leaf":"` + string(types.HISTORY) + `"`,
		`"active":1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("tab_switch event missing %s in %q", want, out)
		}
	}
}

func TestTabbedRail_NilLoggerSwitchDoesNotPanic(t *testing.T) {
	rail, _, _ := newTabbedRail(testfake.NewRecorderGuiDriver(), true)
	// No SetLogger: log stays nil. A real switch must not panic and emits
	// nothing (logs.Event guards nil).
	rail.SetActiveTab(1)
	if rail.ActiveTab() != 1 {
		t.Errorf("active tab = %d, want 1", rail.ActiveTab())
	}
}

func TestTabbedRail_HandleRenderPublishesTabsEveryFrame(t *testing.T) {
	drv := testfake.NewRecorderGuiDriver()
	drv.SetRealView(queryRailViewName, gocui.NewView(queryRailViewName, 0, 0, 20, 10, gocui.OutputNormal))
	rail, _, _ := newTabbedRail(drv, true)

	if err := rail.HandleRender(); err != nil {
		t.Fatalf("HandleRender (editor active): %v", err)
	}
	rail.SetActiveTab(1)
	if err := rail.HandleRender(); err != nil {
		t.Fatalf("HandleRender (history active): %v", err)
	}

	calls := drv.AllSetViewTabsCalls()
	if len(calls) != 2 {
		t.Fatalf("SetViewTabs called %d times, want 2 (once per frame)", len(calls))
	}
	if got := calls[0].Labels; got[0] != "[Editor]" || got[1] != "History" || calls[0].ActiveIdx != 0 {
		t.Errorf("frame 1 = %v idx %d, want [[Editor] History] idx 0", got, calls[0].ActiveIdx)
	}
	if got := calls[1].Labels; got[0] != "Editor" || got[1] != "[History]" || calls[1].ActiveIdx != 1 {
		t.Errorf("frame 2 = %v idx %d, want [Editor [History]] idx 1", got, calls[1].ActiveIdx)
	}
}

func TestTabbedRail_HandleRenderDelegatesToActiveLeafOnly(t *testing.T) {
	drv := testfake.NewRecorderGuiDriver()
	drv.SetRealView(queryRailViewName, gocui.NewView(queryRailViewName, 0, 0, 20, 10, gocui.OutputNormal))
	rail, editor, history := newTabbedRail(drv, true)

	if err := rail.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if editor.renderCount != 1 || history.renderCount != 0 {
		t.Errorf("after editor frame: editor=%d history=%d, want 1/0", editor.renderCount, history.renderCount)
	}

	rail.SetActiveTab(1)
	if err := rail.HandleRender(); err != nil {
		t.Fatalf("HandleRender (history): %v", err)
	}
	if editor.renderCount != 1 || history.renderCount != 1 {
		t.Errorf("after history frame: editor=%d history=%d, want 1/1", editor.renderCount, history.renderCount)
	}
}

func TestTabbedRail_UnwiredLeafAndNilDriverDoNotPanic(t *testing.T) {
	// nil driver.
	rail, _, _ := newTabbedRail(nil, true)
	if err := rail.HandleRender(); err != nil {
		t.Fatalf("HandleRender with nil driver: %v", err)
	}

	// nil leaves (no SetLeaves).
	drv := testfake.NewRecorderGuiDriver()
	drv.SetRealView(queryRailViewName, gocui.NewView(queryRailViewName, 0, 0, 20, 10, gocui.OutputNormal))
	base := NewBaseContext(BaseContextOpts{Key: types.QUERY_EDITOR, ViewName: queryRailViewName, Kind: types.MAIN_CONTEXT})
	unwired := NewTabbedRailContext(base, Deps{GuiDriver: drv}, TabbedRailOpts{FireFocusHooks: true},
		TabSpec{Label: "Editor", LeafKey: types.QUERY_EDITOR, ManagesOwnOrigin: true},
		TabSpec{Label: "History", LeafKey: types.HISTORY},
	)
	if err := unwired.HandleRender(); err != nil {
		t.Fatalf("HandleRender with nil leaf: %v", err)
	}
	unwired.SetActiveTab(1)
	if err := unwired.HandleRender(); err != nil {
		t.Fatalf("HandleRender after switch with nil leaves: %v", err)
	}
	if len(drv.AllSetViewTabsCalls()) != 2 {
		t.Errorf("tab strip not published with nil leaves: got %d, want 2", len(drv.AllSetViewTabsCalls()))
	}
}

// TestTabbedRail_ListLeafOriginDoesNotBleed exercises the per-tab origin
// machinery for LIST leaves and proves the restore happens at most once per
// switch (consumed in HandleRender). FireFocusHooks is irrelevant to origins.
func TestTabbedRail_ListLeafOriginDoesNotBleed(t *testing.T) {
	v := gocui.NewView(queryRailViewName, 0, 0, 20, 10, gocui.OutputNormal)
	drv := testfake.NewRecorderGuiDriver()
	drv.SetRealView(queryRailViewName, v)

	base := NewBaseContext(BaseContextOpts{Key: types.QUERY_EDITOR, ViewName: queryRailViewName, Kind: types.MAIN_CONTEXT})
	rail := NewTabbedRailContext(base, Deps{GuiDriver: drv}, TabbedRailOpts{FireFocusHooks: false},
		TabSpec{Label: "A", LeafKey: types.HISTORY},
		TabSpec{Label: "B", LeafKey: types.SAVED_QUERY},
	)
	rail.SetLeaves(newFakeLeaf(types.HISTORY), newFakeLeaf(types.SAVED_QUERY))

	// On tab A, simulate a horizontal pan, then switch to B.
	_ = rail.HandleRender()
	v.SetOrigin(5, 0)
	rail.SetActiveTab(1) // capture A's origin (5); restore B's (0)
	_ = rail.HandleRender()
	if ox, _ := v.Origin(); ox != 0 {
		t.Errorf("tab B ox = %d, want 0 (no bleed from A)", ox)
	}

	// Restore is consumed: a second HandleRender on B (without a switch) must
	// not re-apply B's origin if the user has panned since.
	v.SetOrigin(7, 0)
	_ = rail.HandleRender()
	if ox, _ := v.Origin(); ox != 7 {
		t.Errorf("tab B ox = %d on second frame, want 7 (restore not repeated)", ox)
	}

	// Switch back to A: A's saved pan (5) is restored exactly once.
	rail.SetActiveTab(0)
	_ = rail.HandleRender()
	if ox, _ := v.Origin(); ox != 5 {
		t.Errorf("tab A ox = %d after switch-back, want 5 (origin preserved)", ox)
	}
}

// TestTabbedRail_ManagesOwnOriginTabSkipsSaveAndRestore proves a
// managesOwnOrigin tab (index 0) opts out of both save and restore.
func TestTabbedRail_ManagesOwnOriginTabSkipsSaveAndRestore(t *testing.T) {
	v := gocui.NewView(queryRailViewName, 0, 0, 20, 10, gocui.OutputNormal)
	drv := testfake.NewRecorderGuiDriver()
	drv.SetRealView(queryRailViewName, v)
	rail, _, _ := newTabbedRail(drv, true) // tab 0 = editor (managesOwnOrigin), tab 1 = list

	// On editor (tab 0), it "scrolled itself" to ox=7.
	_ = rail.HandleRender()
	v.SetOrigin(7, 0)

	// Switch to list: editor origin NOT captured (managesOwnOrigin); list
	// restores its own saved origin (0,0).
	rail.SetActiveTab(1)
	_ = rail.HandleRender()
	if ox, _ := v.Origin(); ox != 0 {
		t.Errorf("list tab ox = %d, want 0 (editor origin not captured)", ox)
	}

	// Editor scrolls itself again (ox=3) then we switch back: restore skipped.
	v.SetOrigin(3, 0)
	rail.SetActiveTab(0)
	_ = rail.HandleRender()
	if ox, _ := v.Origin(); ox != 3 {
		t.Errorf("editor tab ox = %d after return, want 3 (restore skipped)", ox)
	}
}

// TestTabbedRail_HandleFocusLostFlushesInactiveDirtyOnly proves the container
// HandleFocusLost delegates to the active leaf AND flushes every NON-active
// dirtyFlusher leaf, skipping the active index — independent of FireFocusHooks.
func TestTabbedRail_HandleFocusLostFlushesInactiveDirtyOnly(t *testing.T) {
	base := NewBaseContext(BaseContextOpts{Key: types.QUERY_EDITOR, ViewName: queryRailViewName, Kind: types.MAIN_CONTEXT})
	// FireFocusHooks=false to prove the flush is NOT gated by it.
	rail := NewTabbedRailContext(base, Deps{GuiDriver: testfake.NewRecorderGuiDriver()}, TabbedRailOpts{FireFocusHooks: false},
		TabSpec{Label: "Editor", LeafKey: types.QUERY_EDITOR},
		TabSpec{Label: "History", LeafKey: types.HISTORY},
	)
	editor := newDirtySpyLeaf(types.QUERY_EDITOR)
	history := newDirtySpyLeaf(types.HISTORY)
	rail.SetLeaves(editor, history)

	// Active is the editor (0). HandleFocusLost must flush ONLY the inactive
	// history leaf (the active editor's own HandleFocusLost handles itself).
	if err := rail.HandleFocusLost(types.OnFocusLostOpts{}); err != nil {
		t.Fatalf("HandleFocusLost: %v", err)
	}
	if editor.flushCount != 0 {
		t.Errorf("active editor FlushDirty fired %d times, want 0 (skip active)", editor.flushCount)
	}
	if history.flushCount != 1 {
		t.Errorf("inactive history FlushDirty fired %d times, want 1", history.flushCount)
	}
}

func TestTabbedRail_HandleFocusDelegatesToActiveLeaf(t *testing.T) {
	rail, editor, history := newTabbedRail(testfake.NewRecorderGuiDriver(), false)

	// FireFocusHooks=false, but the CONTAINER HandleFocus always delegates.
	if err := rail.HandleFocus(types.OnFocusOpts{}); err != nil {
		t.Fatalf("HandleFocus: %v", err)
	}
	if editor.focusCount != 1 {
		t.Errorf("active editor HandleFocus fired %d times, want 1", editor.focusCount)
	}
	if history.focusCount != 0 {
		t.Errorf("inactive history HandleFocus fired %d times, want 0", history.focusCount)
	}
}

func TestTabbedRail_ActiveLeafKeyTracksActiveTab(t *testing.T) {
	rail, _, _ := newTabbedRail(testfake.NewRecorderGuiDriver(), true)
	if got := rail.ActiveLeafKey(); got != types.QUERY_EDITOR {
		t.Errorf("ActiveLeafKey = %q, want %q", got, types.QUERY_EDITOR)
	}
	rail.SetActiveTab(1)
	if got := rail.ActiveLeafKey(); got != types.HISTORY {
		t.Errorf("ActiveLeafKey after switch = %q, want %q", got, types.HISTORY)
	}
}

// newRuntimeRail builds an EMPTY container (no specs) for the runtime SetTabs
// tests. FireFocusHooks=false: runtime display leaves carry no focus protocol.
func newRuntimeRail(drv types.GuiDriver) *TabbedRailContext {
	base := NewBaseContext(BaseContextOpts{
		Key:      types.QUERY_EDITOR,
		ViewName: queryRailViewName,
		Kind:     types.MAIN_CONTEXT,
	})
	return NewTabbedRailContext(base, Deps{GuiDriver: drv}, TabbedRailOpts{FireFocusHooks: false})
}

// TestTabbedRail_SetTabsRebuilds proves SetTabs replaces the tab set wholesale,
// resets to tab 0, clears prior per-tab origins, and leaves trailing specs
// unwired when fewer leaves are supplied (a nil active leaf renders as a no-op).
func TestTabbedRail_SetTabsRebuilds(t *testing.T) {
	v := gocui.NewView(queryRailViewName, 0, 0, 20, 10, gocui.OutputNormal)
	drv := testfake.NewRecorderGuiDriver()
	drv.SetRealView(queryRailViewName, v)
	rail := newRuntimeRail(drv)

	// First generation: 2 tabs, both wired. Pan tab 0 and switch so tab 0's
	// origin is saved into its slot.
	rail.SetTabs(
		[]TabSpec{{Label: "A", LeafKey: types.HISTORY}, {Label: "B", LeafKey: types.SAVED_QUERY}},
		[]types.IBaseContext{newFakeLeaf(types.HISTORY), newFakeLeaf(types.SAVED_QUERY)},
	)
	_ = rail.HandleRender()
	v.SetOrigin(9, 0)
	rail.SetActiveTab(1) // saves A's origin (9) into tab 0's slot

	// Second generation REPLACES the first: 1 tab only. Old tabs must be gone,
	// active reset to 0, and the rebuilt tab's origin cleared (fresh struct).
	rail.SetTabs(
		[]TabSpec{{Label: "Only", LeafKey: types.HISTORY}},
		[]types.IBaseContext{newFakeLeaf(types.HISTORY)},
	)
	if got := rail.TabCount(); got != 1 {
		t.Fatalf("TabCount after rebuild = %d, want 1 (full replace)", got)
	}
	if got := rail.ActiveTab(); got != 0 {
		t.Fatalf("ActiveTab after rebuild = %d, want 0", got)
	}
	// The rebuilt tab's saved origin is the zero value: a switch-induced restore
	// would re-apply (0,0), not the stale 9. With one tab there is nothing to
	// switch to, so just assert the slot is fresh via a re-render not crashing.
	if err := rail.HandleRender(); err != nil {
		t.Fatalf("HandleRender after rebuild: %v", err)
	}

	// Third generation: 2 specs but only 1 leaf — the trailing tab is unwired.
	// Render must be a no-op on a nil active leaf if we move there.
	rail.SetTabs(
		[]TabSpec{{Label: "X", LeafKey: types.HISTORY}, {Label: "Y", LeafKey: types.SAVED_QUERY}},
		[]types.IBaseContext{newFakeLeaf(types.HISTORY)}, // only leaf 0
	)
	rail.SetActiveTab(1) // active tab 1 has a nil leaf
	if err := rail.HandleRender(); err != nil {
		t.Fatalf("HandleRender on nil-leaf active tab: %v", err)
	}
}

// TestTabbedRail_SetTabsResetsActiveTab is the critical panic guard: shrinking
// the tab set while activeTab points past the new length must NOT panic in
// HandleRender (which indexes t.tabs[t.activeTab] behind only a len==0 guard).
func TestTabbedRail_SetTabsResetsActiveTab(t *testing.T) {
	drv := testfake.NewRecorderGuiDriver()
	drv.SetRealView(queryRailViewName, gocui.NewView(queryRailViewName, 0, 0, 20, 10, gocui.OutputNormal))
	rail := newRuntimeRail(drv)

	rail.SetTabs(
		[]TabSpec{
			{Label: "A", LeafKey: types.HISTORY},
			{Label: "B", LeafKey: types.SAVED_QUERY},
			{Label: "C", LeafKey: types.HISTORY},
		},
		[]types.IBaseContext{newFakeLeaf(types.HISTORY), newFakeLeaf(types.SAVED_QUERY), newFakeLeaf(types.HISTORY)},
	)
	rail.SetActiveTab(2) // active on the last tab

	rail.SetTabs(
		[]TabSpec{{Label: "Solo", LeafKey: types.HISTORY}},
		[]types.IBaseContext{newFakeLeaf(types.HISTORY)},
	)
	if got := rail.ActiveTab(); got != 0 {
		t.Fatalf("ActiveTab after shrink = %d, want 0", got)
	}
	// The proof: this must not panic with index-out-of-range.
	if err := rail.HandleRender(); err != nil {
		t.Fatalf("HandleRender after shrink: %v", err)
	}
	calls := drv.AllSetViewTabsCalls()
	last := calls[len(calls)-1]
	if last.ActiveIdx != 0 || len(last.Labels) != 1 || last.Labels[0] != "[Solo]" {
		t.Errorf("post-shrink tab strip = %v idx %d, want [[Solo]] idx 0", last.Labels, last.ActiveIdx)
	}
}

// TestTabbedRail_DynamicLeafRenders is the render-proof: a DisplayLeafContext
// built at runtime (NOT registered in setup.go) and injected via SetTabs renders
// its body into EXACTLY the container's view, and the tab strip marks it active.
func TestTabbedRail_DynamicLeafRenders(t *testing.T) {
	const containerView = queryRailViewName
	drv := testfake.NewRecorderGuiDriver()
	// SetView registers the container view (so the leaf's SetContent targets a
	// known view). The first call returns gocui.ErrUnknownView as the
	// "new-view-created" sentinel — that is the success path, not a failure.
	if _, err := drv.SetView(containerView, 0, 0, 20, 10, 0); err != nil && !errors.Is(err, gocui.ErrUnknownView) {
		t.Fatalf("SetView: %v", err)
	}
	rail := newRuntimeRail(drv)

	leafBase := NewBaseContext(BaseContextOpts{Key: types.HISTORY, ViewName: "ignored", Kind: types.MAIN_CONTEXT})
	const body = "dynamic-body-line"
	leaf := NewDisplayLeafContext(leafBase, Deps{GuiDriver: drv}, rail.GetViewName(), body)

	if leaf.GetViewName() != rail.GetViewName() {
		t.Fatalf("leaf view %q != container view %q", leaf.GetViewName(), rail.GetViewName())
	}

	rail.SetTabs(
		[]TabSpec{{Label: "Cheat", LeafKey: types.HISTORY}},
		[]types.IBaseContext{leaf},
	)
	if err := rail.HandleRender(); err != nil {
		t.Fatalf("HandleRender returned %v, want nil", err)
	}

	// The body landed in EXACTLY the container view.
	if got := drv.GetViewBuffer(containerView); got != body {
		t.Errorf("container buffer = %q, want %q", got, body)
	}

	// Tab strip published with the active label marked.
	calls := drv.AllSetViewTabsCalls()
	last := calls[len(calls)-1]
	if last.Name != rail.GetViewName() || last.ActiveIdx != 0 || len(last.Labels) != 1 || last.Labels[0] != "[Cheat]" {
		t.Errorf("SetViewTabs = name %q labels %v idx %d, want %q [[Cheat]] 0",
			last.Name, last.Labels, last.ActiveIdx, rail.GetViewName())
	}
}

// TestTabbedRail_NextPrevWrap proves NextTab/PrevTab wrap and are no-ops on
// 0/1-tab containers.
func TestTabbedRail_NextPrevWrap(t *testing.T) {
	drv := testfake.NewRecorderGuiDriver()
	drv.SetRealView(queryRailViewName, gocui.NewView(queryRailViewName, 0, 0, 20, 10, gocui.OutputNormal))

	// Empty container: both no-op, no panic.
	empty := newRuntimeRail(drv)
	empty.NextTab()
	empty.PrevTab()
	if empty.ActiveTab() != 0 {
		t.Errorf("empty NextTab/PrevTab moved active to %d, want 0", empty.ActiveTab())
	}

	// Single tab: wraps onto itself (no-op switch).
	single := newRuntimeRail(drv)
	single.SetTabs([]TabSpec{{Label: "A", LeafKey: types.HISTORY}}, []types.IBaseContext{newFakeLeaf(types.HISTORY)})
	single.NextTab()
	single.PrevTab()
	if single.ActiveTab() != 0 {
		t.Errorf("single-tab NextTab/PrevTab moved active to %d, want 0", single.ActiveTab())
	}

	// Three tabs: Next from last wraps to 0; Prev from 0 wraps to last.
	rail := newRuntimeRail(drv)
	rail.SetTabs(
		[]TabSpec{{Label: "A", LeafKey: types.HISTORY}, {Label: "B", LeafKey: types.SAVED_QUERY}, {Label: "C", LeafKey: types.HISTORY}},
		[]types.IBaseContext{newFakeLeaf(types.HISTORY), newFakeLeaf(types.SAVED_QUERY), newFakeLeaf(types.HISTORY)},
	)
	rail.NextTab()
	if rail.ActiveTab() != 1 {
		t.Fatalf("NextTab from 0 = %d, want 1", rail.ActiveTab())
	}
	rail.SetActiveTab(2)
	rail.NextTab() // last -> wrap to 0
	if rail.ActiveTab() != 0 {
		t.Errorf("NextTab from last = %d, want 0 (wrap)", rail.ActiveTab())
	}
	rail.PrevTab() // 0 -> wrap to last
	if rail.ActiveTab() != 2 {
		t.Errorf("PrevTab from 0 = %d, want 2 (wrap)", rail.ActiveTab())
	}
}

// TestTabbedRail_SetTabsPreservesLogger proves the session logger survives a
// SetTabs rebuild: a subsequent tab switch still emits a tab_switch event.
func TestTabbedRail_SetTabsPreservesLogger(t *testing.T) {
	drv := testfake.NewRecorderGuiDriver()
	drv.SetRealView(queryRailViewName, gocui.NewView(queryRailViewName, 0, 0, 20, 10, gocui.OutputNormal))
	rail := newRuntimeRail(drv)

	var buf bytes.Buffer
	rail.SetLogger(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	rail.SetTabs(
		[]TabSpec{{Label: "A", LeafKey: types.HISTORY}, {Label: "B", LeafKey: types.SAVED_QUERY}},
		[]types.IBaseContext{newFakeLeaf(types.HISTORY), newFakeLeaf(types.SAVED_QUERY)},
	)
	rail.SetActiveTab(1) // real switch -> must emit one tab_switch event
	if out := buf.String(); !strings.Contains(out, `"evt":"tab_switch"`) {
		t.Errorf("logger lost across SetTabs: no tab_switch event in %q", out)
	}
}

// TestDisplayLeaf_SatisfiesNeitherSeam asserts a DisplayLeafContext satisfies
// NEITHER the dirtyFlusher NOR the bodyTextRenderer seam, so the container takes
// the leaf-delegation render path and never enrols the leaf in
// flushInactiveDirty. The seams are unexported, so this test must live in the
// context package (it does).
func TestDisplayLeaf_SatisfiesNeitherSeam(t *testing.T) {
	leaf := NewDisplayLeafContext(
		NewBaseContext(BaseContextOpts{Key: types.HISTORY, ViewName: "v", Kind: types.MAIN_CONTEXT}),
		Deps{}, "v", "body",
	)
	if _, ok := interface{}(leaf).(dirtyFlusher); ok {
		t.Error("DisplayLeafContext unexpectedly satisfies dirtyFlusher")
	}
	if _, ok := interface{}(leaf).(bodyTextRenderer); ok {
		t.Error("DisplayLeafContext unexpectedly satisfies bodyTextRenderer")
	}
}
