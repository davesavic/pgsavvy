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

const queryRailViewName = "query_editor"

// fakeLeaf is a stateless leaf context that counts focus / focus-lost / render
// calls so the container's single-fire hook guarantees can be asserted. It
// renders nothing into the shared view (the container only delegates the
// render call; the body content is not under test here).
type fakeLeaf struct {
	BaseContext
	focusCount     int
	focusLostCount int
	renderCount    int
}

func newFakeLeaf(key types.ContextKey) *fakeLeaf {
	return &fakeLeaf{
		BaseContext: NewBaseContext(BaseContextOpts{
			Key:      key,
			ViewName: queryRailViewName,
			Kind:     types.MAIN_CONTEXT,
		}),
	}
}

func (l *fakeLeaf) HandleFocus(_ types.OnFocusOpts) error { l.focusCount++; return nil }
func (l *fakeLeaf) HandleFocusLost(_ types.OnFocusLostOpts) error {
	l.focusLostCount++
	return nil
}
func (l *fakeLeaf) HandleRender() error { l.renderCount++; return nil }

// newQueryRail builds a container with two tabs: an editor leaf (index 0,
// managesOwnOrigin) and a list leaf (index 1). The editor leaf models the
// query editor (drives its own scroll); the list leaf gets the generic
// origin machinery.
func newQueryRail(drv types.GuiDriver) (*QueryRailContext, *fakeLeaf, *fakeLeaf) {
	base := NewBaseContext(BaseContextOpts{
		Key:      types.QUERY_EDITOR,
		ViewName: queryRailViewName,
		Kind:     types.MAIN_CONTEXT,
	})
	rail := NewQueryRailContext(base, Deps{GuiDriver: drv},
		QueryRailTabSpec{Label: "Editor", LeafKey: types.QUERY_EDITOR, ManagesOwnOrigin: true},
		QueryRailTabSpec{Label: "History", LeafKey: types.HISTORY},
	)
	editor := newFakeLeaf(types.QUERY_EDITOR)
	history := newFakeLeaf(types.HISTORY)
	rail.SetLeaves(editor, history)
	return rail, editor, history
}

func TestQueryRail_DefaultsToFirstTab(t *testing.T) {
	rail, _, _ := newQueryRail(testfake.NewRecorderGuiDriver())
	if got := rail.ActiveTab(); got != 0 {
		t.Fatalf("default active tab = %d, want 0", got)
	}
	if got := rail.ActiveLeafKey(); got != types.QUERY_EDITOR {
		t.Fatalf("default ActiveLeafKey = %q, want %q", got, types.QUERY_EDITOR)
	}
}

func TestQueryRail_SetActiveTabClampsWithoutPanic(t *testing.T) {
	rail, _, _ := newQueryRail(testfake.NewRecorderGuiDriver())

	rail.SetActiveTab(99)
	if got := rail.ActiveTab(); got != 1 {
		t.Errorf("SetActiveTab(99) => %d, want clamp to 1 (last tab)", got)
	}
	rail.SetActiveTab(-3)
	if got := rail.ActiveTab(); got != 0 {
		t.Errorf("SetActiveTab(-3) => %d, want clamp to 0 (first tab)", got)
	}
}

func TestQueryRail_NoOpSwitchDoesNotFireHooks(t *testing.T) {
	rail, editor, history := newQueryRail(testfake.NewRecorderGuiDriver())

	// active is already 0; switching to 0 must be a no-op.
	rail.SetActiveTab(0)
	if editor.focusCount != 0 || editor.focusLostCount != 0 {
		t.Errorf("no-op switch fired editor hooks: focus=%d focusLost=%d, want 0/0",
			editor.focusCount, editor.focusLostCount)
	}
	if history.focusCount != 0 || history.focusLostCount != 0 {
		t.Errorf("no-op switch fired history hooks: focus=%d focusLost=%d, want 0/0",
			history.focusCount, history.focusLostCount)
	}
}

// TestQueryRail_ClampToCurrentTabFiresNoHooks proves the no-op guard holds
// when an over-range index clamps onto the already-active tab: no focus hooks
// fire and the active index is unchanged.
func TestQueryRail_ClampToCurrentTabFiresNoHooks(t *testing.T) {
	rail, editor, history := newQueryRail(testfake.NewRecorderGuiDriver())

	rail.SetActiveTab(1) // real switch to the last tab
	editor.focusLostCount = 0
	history.focusCount = 0

	// Already on the last tab; an over-range index clamps to the current tab
	// and must be a silent no-op (no hooks fire).
	rail.SetActiveTab(99)
	if editor.focusCount != 0 || editor.focusLostCount != 0 ||
		history.focusCount != 0 || history.focusLostCount != 0 {
		t.Errorf("clamp-to-current fired hooks: editor f=%d fl=%d, history f=%d fl=%d, want all 0",
			editor.focusCount, editor.focusLostCount, history.focusCount, history.focusLostCount)
	}
	if rail.ActiveTab() != 1 {
		t.Errorf("active tab = %d after clamp-to-current, want still 1", rail.ActiveTab())
	}
}

// TestQueryRail_TabSwitchEmitsMetadataOnlyEvent proves SetActiveTab emits a
// single tab_switch event on a real switch (and none on a no-op), carrying
// METADATA ONLY (from/to leaf keys + active index) — never SQL or buffer text.
func TestQueryRail_TabSwitchEmitsMetadataOnlyEvent(t *testing.T) {
	rail, _, _ := newQueryRail(testfake.NewRecorderGuiDriver())
	var buf bytes.Buffer
	rail.SetLogger(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	// No-op switch (already on tab 0) emits nothing.
	rail.SetActiveTab(0)
	if buf.Len() != 0 {
		t.Fatalf("no-op switch emitted a log line: %q", buf.String())
	}

	// Real switch emits exactly one tab_switch event.
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

func TestQueryRail_RealSwitchFiresHooksExactlyOnce(t *testing.T) {
	rail, editor, history := newQueryRail(testfake.NewRecorderGuiDriver())

	rail.SetActiveTab(1) // editor -> history

	if editor.focusLostCount != 1 {
		t.Errorf("outgoing editor HandleFocusLost fired %d times, want 1", editor.focusLostCount)
	}
	if editor.focusCount != 0 {
		t.Errorf("outgoing editor HandleFocus fired %d times, want 0", editor.focusCount)
	}
	if history.focusCount != 1 {
		t.Errorf("incoming history HandleFocus fired %d times, want 1", history.focusCount)
	}
	if history.focusLostCount != 0 {
		t.Errorf("incoming history HandleFocusLost fired %d times, want 0", history.focusLostCount)
	}
}

func TestQueryRail_HandleRenderPublishesTabsWithActiveMarked(t *testing.T) {
	drv := testfake.NewRecorderGuiDriver()
	drv.SetRealView(queryRailViewName, gocui.NewView(queryRailViewName, 0, 0, 20, 10, gocui.OutputNormal))
	rail, _, _ := newQueryRail(drv)

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
	for _, c := range calls {
		if c.Name != queryRailViewName {
			t.Errorf("SetViewTabs name = %q, want %q", c.Name, queryRailViewName)
		}
		if len(c.Labels) != 2 {
			t.Errorf("SetViewTabs labels = %v, want 2", c.Labels)
		}
	}
	// Frame 1: editor active -> "[Editor]", "History".
	if got := calls[0].Labels; got[0] != "[Editor]" || got[1] != "History" {
		t.Errorf("frame 1 labels = %v, want [[Editor] History]", got)
	}
	if calls[0].ActiveIdx != 0 {
		t.Errorf("frame 1 active = %d, want 0", calls[0].ActiveIdx)
	}
	// Frame 2: history active -> "Editor", "[History]".
	if got := calls[1].Labels; got[0] != "Editor" || got[1] != "[History]" {
		t.Errorf("frame 2 labels = %v, want [Editor [History]]", got)
	}
	if calls[1].ActiveIdx != 1 {
		t.Errorf("frame 2 active = %d, want 1", calls[1].ActiveIdx)
	}
}

func TestQueryRail_HandleRenderDelegatesToActiveLeaf(t *testing.T) {
	drv := testfake.NewRecorderGuiDriver()
	drv.SetRealView(queryRailViewName, gocui.NewView(queryRailViewName, 0, 0, 20, 10, gocui.OutputNormal))
	rail, editor, history := newQueryRail(drv)

	if err := rail.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if editor.renderCount != 1 {
		t.Errorf("active editor leaf rendered %d times, want 1", editor.renderCount)
	}
	if history.renderCount != 0 {
		t.Errorf("inactive history leaf rendered %d times, want 0 (no-op)", history.renderCount)
	}

	rail.SetActiveTab(1)
	if err := rail.HandleRender(); err != nil {
		t.Fatalf("HandleRender (history): %v", err)
	}
	if history.renderCount != 1 {
		t.Errorf("active history leaf rendered %d times, want 1", history.renderCount)
	}
	if editor.renderCount != 1 {
		t.Errorf("inactive editor leaf rendered %d more times, want still 1", editor.renderCount)
	}
}

func TestQueryRail_UnwiredLeafRenderIsNoOp(t *testing.T) {
	drv := testfake.NewRecorderGuiDriver()
	drv.SetRealView(queryRailViewName, gocui.NewView(queryRailViewName, 0, 0, 20, 10, gocui.OutputNormal))
	base := NewBaseContext(BaseContextOpts{
		Key:      types.QUERY_EDITOR,
		ViewName: queryRailViewName,
		Kind:     types.MAIN_CONTEXT,
	})
	rail := NewQueryRailContext(base, Deps{GuiDriver: drv},
		QueryRailTabSpec{Label: "Editor", LeafKey: types.QUERY_EDITOR, ManagesOwnOrigin: true},
		QueryRailTabSpec{Label: "History", LeafKey: types.HISTORY},
	)
	// No SetLeaves: leaves stay nil.

	if err := rail.HandleRender(); err != nil {
		t.Fatalf("HandleRender with nil leaf: %v", err)
	}
	// Switching past a nil leaf must not panic either.
	rail.SetActiveTab(1)
	if err := rail.HandleRender(); err != nil {
		t.Fatalf("HandleRender after switch with nil leaves: %v", err)
	}
	// The tab strip is still published despite nil leaves.
	if len(drv.AllSetViewTabsCalls()) != 2 {
		t.Errorf("SetViewTabs not published with nil leaves: got %d calls, want 2",
			len(drv.AllSetViewTabsCalls()))
	}
}

func TestQueryRail_NoDriverHandleRenderIsNoOp(t *testing.T) {
	rail, _, _ := newQueryRail(nil)
	if err := rail.HandleRender(); err != nil {
		t.Fatalf("HandleRender with nil driver: %v", err)
	}
}

// TestQueryRail_ListLeafOriginDoesNotBleed exercises the per-tab origin
// machinery for a LIST leaf. Tab index 1 (History) is a normal list tab.
// We add a third list tab so two origin-managed tabs can swap without the
// editor's opt-out in the way.
func TestQueryRail_ListLeafOriginDoesNotBleed(t *testing.T) {
	v := gocui.NewView(queryRailViewName, 0, 0, 20, 10, gocui.OutputNormal)
	drv := testfake.NewRecorderGuiDriver()
	drv.SetRealView(queryRailViewName, v)

	base := NewBaseContext(BaseContextOpts{
		Key:      types.QUERY_EDITOR,
		ViewName: queryRailViewName,
		Kind:     types.MAIN_CONTEXT,
	})
	rail := NewQueryRailContext(base, Deps{GuiDriver: drv},
		QueryRailTabSpec{Label: "Editor", LeafKey: types.QUERY_EDITOR, ManagesOwnOrigin: true},
		QueryRailTabSpec{Label: "A", LeafKey: types.HISTORY},
		QueryRailTabSpec{Label: "B", LeafKey: types.SAVED_QUERY},
	)
	rail.SetLeaves(newFakeLeaf(types.QUERY_EDITOR), newFakeLeaf(types.HISTORY), newFakeLeaf(types.SAVED_QUERY))

	// Go to list tab A and simulate a horizontal pan (ox = 5).
	rail.SetActiveTab(1)
	_ = rail.HandleRender()
	v.SetOrigin(5, 0)

	// Switch to list tab B: the switch captures A's origin; B starts at its
	// saved origin (0,0).
	rail.SetActiveTab(2)
	_ = rail.HandleRender()
	if ox, _ := v.Origin(); ox != 0 {
		t.Errorf("tab B ox = %d, want 0 (no bleed from A's pan)", ox)
	}

	// Pan B (ox = 9) then switch back to A: A's saved pan (5) is restored.
	v.SetOrigin(9, 0)
	rail.SetActiveTab(1)
	_ = rail.HandleRender()
	if ox, _ := v.Origin(); ox != 5 {
		t.Errorf("tab A ox = %d after switch-back, want 5 (origin preserved)", ox)
	}

	// And switching to B again restores B's saved pan (9).
	rail.SetActiveTab(2)
	_ = rail.HandleRender()
	if ox, _ := v.Origin(); ox != 9 {
		t.Errorf("tab B ox = %d after second visit, want 9 (origin preserved)", ox)
	}
}

// TestQueryRail_EditorLeafNeverGetsStaleOriginRestored proves the editor tab
// (managesOwnOrigin) opts out of restore: a saved origin written while on a
// list tab is NOT applied to the shared view when switching INTO the editor
// tab, so the editor's own scroll-to-cursor is not fought.
func TestQueryRail_EditorLeafNeverGetsStaleOriginRestored(t *testing.T) {
	v := gocui.NewView(queryRailViewName, 0, 0, 20, 10, gocui.OutputNormal)
	drv := testfake.NewRecorderGuiDriver()
	drv.SetRealView(queryRailViewName, v)
	rail, _, _ := newQueryRail(drv) // tab 0 = editor (managesOwnOrigin), tab 1 = list

	// Start on the editor (tab 0). The editor "scrolled itself" to ox=7.
	if err := rail.HandleRender(); err != nil {
		t.Fatalf("HandleRender (editor): %v", err)
	}
	v.SetOrigin(7, 0)

	// Switch to the list tab: leaving the editor must NOT capture the
	// editor's origin (managesOwnOrigin), and the list tab restores its own
	// saved origin (0,0).
	rail.SetActiveTab(1)
	if err := rail.HandleRender(); err != nil {
		t.Fatalf("HandleRender (list): %v", err)
	}
	if ox, _ := v.Origin(); ox != 0 {
		t.Errorf("list tab ox = %d, want 0 (editor origin not captured)", ox)
	}

	// The editor scrolls itself again on the list tab's view space; say ox=3.
	v.SetOrigin(3, 0)

	// Switch BACK to the editor: restore must be skipped (managesOwnOrigin),
	// so whatever origin is live is left untouched — the editor's own
	// scroll, not a stale saved value, drives the view.
	rail.SetActiveTab(0)
	if err := rail.HandleRender(); err != nil {
		t.Fatalf("HandleRender (editor return): %v", err)
	}
	if ox, _ := v.Origin(); ox != 3 {
		t.Errorf("editor tab ox = %d after return, want 3 (origin left to editor, not restored)", ox)
	}
}

func TestQueryRail_ActiveLeafKeyTracksActiveTab(t *testing.T) {
	rail, _, _ := newQueryRail(testfake.NewRecorderGuiDriver())
	if got := rail.ActiveLeafKey(); got != types.QUERY_EDITOR {
		t.Errorf("ActiveLeafKey = %q, want %q", got, types.QUERY_EDITOR)
	}
	rail.SetActiveTab(1)
	if got := rail.ActiveLeafKey(); got != types.HISTORY {
		t.Errorf("ActiveLeafKey after switch = %q, want %q", got, types.HISTORY)
	}
}

// TestQueryRail_StatelessLeavesDoNotError confirms a switch between leaves
// whose focus hooks are pure no-ops (the BaseContext defaults) returns
// without error and the active index advances.
func TestQueryRail_StatelessLeavesDoNotError(t *testing.T) {
	base := NewBaseContext(BaseContextOpts{
		Key:      types.QUERY_EDITOR,
		ViewName: queryRailViewName,
		Kind:     types.MAIN_CONTEXT,
	})
	rail := NewQueryRailContext(base, Deps{GuiDriver: testfake.NewRecorderGuiDriver()},
		QueryRailTabSpec{Label: "A", LeafKey: types.HISTORY},
		QueryRailTabSpec{Label: "B", LeafKey: types.SAVED_QUERY},
	)
	// Plain BaseContext leaves: HandleFocus/HandleFocusLost are no-ops.
	a := NewBaseContext(BaseContextOpts{Key: types.HISTORY, ViewName: queryRailViewName})
	b := NewBaseContext(BaseContextOpts{Key: types.SAVED_QUERY, ViewName: queryRailViewName})
	rail.SetLeaves(&a, &b)

	rail.SetActiveTab(1)
	if rail.ActiveTab() != 1 {
		t.Fatalf("active tab = %d after switch, want 1", rail.ActiveTab())
	}
}
