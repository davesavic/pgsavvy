package context

import (
	"bytes"
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
