package orchestrator_test

import (
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/theme"
)

// lastSetViewTabsFor returns the most recent SetViewTabs call recorded for the
// named view, or nil when none was made.
func lastSetViewTabsFor(rec *testfake.RecorderGuiDriver, view string) *testfake.SetViewTabsCall {
	var last *testfake.SetViewTabsCall
	for i := range rec.AllSetViewTabsCalls() {
		c := rec.AllSetViewTabsCalls()[i]
		if c.Name == view {
			last = &c
		}
	}
	return last
}

// TestQueryRailConsolidatedLayout asserts the many-contexts-ONE-view topology
// for the query pane: the QUERY_RAIL container is the sole renderer of the
// single "query_editor" view, with exactly one SetView for it per frame.
func TestQueryRailConsolidatedLayout(t *testing.T) {
	g, rec := buildTestGui(t)
	if err := g.ContextTree().Push(g.Registry().QueryRail); err != nil {
		t.Fatalf("Push(QueryRail): %v", err)
	}
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}

	got := 0
	for _, c := range rec.AllSetViewCalls() {
		if c.Name == context.QueryRailViewName {
			got++
		}
	}
	if got != 1 {
		t.Errorf("SetView(%q) count = %d, want exactly 1", context.QueryRailViewName, got)
	}
}

// TestQueryRailScopedMasterEditorSwap proves the per-tab master-editor swap:
// the editor attached to the shared rail view equals masterEditors[ActiveLeafKey()]
// for the active tab — QUERY_EDITOR's VimEditor on the editor tab, and the list
// leaf's scoped editor (SAVED_QUERY / HISTORY) when a list tab is active.
func TestQueryRailScopedMasterEditorSwap(t *testing.T) {
	cases := []struct {
		name   string
		tab    int
		expect types.ContextKey
	}{
		{"editor tab", 0, types.QUERY_EDITOR},
		{"saved-query tab", 1, types.SAVED_QUERY},
		{"history tab", 2, types.HISTORY},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, rec := buildTestGui(t)
			if err := g.ContextTree().Push(g.Registry().QueryRail); err != nil {
				t.Fatalf("Push(QueryRail): %v", err)
			}
			g.Registry().QueryRail.SetActiveTab(tc.tab)
			if err := g.RunLayout(120, 40); err != nil {
				t.Fatalf("RunLayout: %v", err)
			}

			if got := g.Registry().QueryRail.ActiveLeafKey(); got != tc.expect {
				t.Fatalf("ActiveLeafKey() = %v, want %v", got, tc.expect)
			}
			attached, ok := rec.InstalledEditors()[context.QueryRailViewName]
			if !ok {
				t.Fatalf("no master editor attached to %q", context.QueryRailViewName)
			}
			want := g.MasterEditorForTest(tc.expect)
			if want == nil {
				t.Fatalf("no master editor built for %v", tc.expect)
			}
			if attached != want {
				t.Errorf("attached editor != masterEditors[%v]; the scoped swap targeted the wrong leaf", tc.expect)
			}
		})
	}
}

// TestQueryRailTabStripReflectsActiveTab asserts container.HandleRender
// publishes the tab strip every frame with the active label bracketed and the
// correct active index for the active tab.
func TestQueryRailTabStripReflectsActiveTab(t *testing.T) {
	cases := []struct {
		name       string
		tab        int
		wantLabels []string
		wantActive int
	}{
		{"editor", 0, []string{"[Query Editor]", "Saved Queries", "History"}, 0},
		{"saved", 1, []string{"Query Editor", "[Saved Queries]", "History"}, 1},
		{"history", 2, []string{"Query Editor", "Saved Queries", "[History]"}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, rec := buildTestGui(t)
			if err := g.ContextTree().Push(g.Registry().QueryRail); err != nil {
				t.Fatalf("Push(QueryRail): %v", err)
			}
			g.Registry().QueryRail.SetActiveTab(tc.tab)
			if err := g.RunLayout(120, 40); err != nil {
				t.Fatalf("RunLayout: %v", err)
			}
			last := lastSetViewTabsFor(rec, context.QueryRailViewName)
			if last == nil {
				t.Fatalf("no SetViewTabs call for %q", context.QueryRailViewName)
			}
			if last.ActiveIdx != tc.wantActive {
				t.Errorf("active idx = %d, want %d", last.ActiveIdx, tc.wantActive)
			}
			if len(last.Labels) != len(tc.wantLabels) {
				t.Fatalf("labels = %v, want %v", last.Labels, tc.wantLabels)
			}
			for i := range tc.wantLabels {
				if last.Labels[i] != tc.wantLabels[i] {
					t.Errorf("label[%d] = %q, want %q", i, last.Labels[i], tc.wantLabels[i])
				}
			}
		})
	}
}

// TestQueryRailTabColorsApplied asserts the orchestrator applies the native
// active/inactive tab colours to the rail view each frame (ColorDefault for
// inactive so list rows stay at the terminal foreground).
func TestQueryRailTabColorsApplied(t *testing.T) {
	g, rec := buildTestGui(t)
	if err := g.ContextTree().Push(g.Registry().QueryRail); err != nil {
		t.Fatalf("Push(QueryRail): %v", err)
	}
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	var last *testfake.SetViewTabColorsCall
	for i := range rec.AllSetViewTabColorsCalls() {
		c := rec.AllSetViewTabColorsCalls()[i]
		if c.Name == context.QueryRailViewName {
			last = &c
		}
	}
	if last == nil {
		t.Fatalf("no SetViewTabColors call for %q", context.QueryRailViewName)
	}
	wantActive := themeFrameAttr(theme.Current().ActiveBorder)
	if last.ActiveFg != wantActive || last.InactiveFg != gocui.ColorDefault {
		t.Errorf("tab colours = (active %v, inactive %v), want (active %v, inactive ColorDefault)",
			last.ActiveFg, last.InactiveFg, wantActive)
	}
}

// TestQueryRailTabClickSwitchesViaUIThread drives the native tab-click binding
// via the recorder's FeedTabClick hook; the click must switch the container's
// active tab (the switch is marshalled through OnUIThread).
func TestQueryRailTabClickSwitchesViaUIThread(t *testing.T) {
	g, rec := buildTestGui(t)
	if got := g.Registry().QueryRail.ActiveTab(); got != 0 {
		t.Fatalf("pre-click active tab = %d, want 0 (editor)", got)
	}
	if err := rec.FeedTabClick(context.QueryRailViewName, 2); err != nil {
		t.Fatalf("FeedTabClick(History): %v", err)
	}
	if got := g.Registry().QueryRail.ActiveTab(); got != 2 {
		t.Errorf("post-click active tab = %d, want 2 (History)", got)
	}
	if err := rec.FeedTabClick(context.QueryRailViewName, 0); err != nil {
		t.Fatalf("FeedTabClick(Query Editor): %v", err)
	}
	if got := g.Registry().QueryRail.ActiveTab(); got != 0 {
		t.Errorf("post-click active tab = %d, want 0 (editor)", got)
	}
}

// TestQueryRailEditorTabInsertsIntoBuffer proves the editor tab routes keys
// into the canonical buffer: with the editor tab active and its VimEditor
// attached, an i (insert) + 'x' rune mutates the QUERY_EDITOR buffer.
func TestQueryRailEditorTabInsertsIntoBuffer(t *testing.T) {
	g, _ := buildTestGui(t)
	if err := g.ContextTree().Push(g.Registry().QueryRail); err != nil {
		t.Fatalf("Push(QueryRail): %v", err)
	}
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	ed := g.MasterEditorForTest(types.QUERY_EDITOR)
	if ed == nil {
		t.Fatal("no master editor installed for QUERY_EDITOR")
	}
	// Enter insert mode then type a rune; the VimEditor writes into the buffer.
	ed.Edit(nil, gocui.NewKeyRune('i'))
	ed.Edit(nil, gocui.NewKeyRune('x'))
	if got := g.Registry().QueryEditor.Buffer().String(); got != "x" {
		t.Errorf("buffer = %q after editor-tab insert, want %q", got, "x")
	}
}

// TestQueryRailListTabDispatchNoBufferWrite proves a list tab does NOT route
// keys into the editor buffer: with a list tab active, its scoped editor is
// attached, so j/k/dd dispatch under the list leaf scope and never mutate the
// QUERY_EDITOR buffer. (Full j/k cursor behaviour is T5; here we lock the
// scope-isolation invariant — list keys must not bleed into the editor.)
func TestQueryRailListTabDispatchNoBufferWrite(t *testing.T) {
	g, _ := buildTestGui(t)
	if err := g.ContextTree().Push(g.Registry().QueryRail); err != nil {
		t.Fatalf("Push(QueryRail): %v", err)
	}
	g.Registry().QueryRail.SetActiveTab(2) // History
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	ed := g.MasterEditorForTest(types.HISTORY)
	if ed == nil {
		t.Fatal("no master editor installed for HISTORY")
	}
	// j/k/dd under the list scope must not panic and must not write the buffer.
	ed.Edit(nil, gocui.NewKeyRune('j'))
	ed.Edit(nil, gocui.NewKeyRune('k'))
	ed.Edit(nil, gocui.NewKeyRune('d'))
	ed.Edit(nil, gocui.NewKeyRune('d'))
	if got := g.Registry().QueryEditor.Buffer().String(); got != "" {
		t.Errorf("editor buffer = %q after list-tab keys; list keys leaked into the editor", got)
	}
}

// TestQueryRailSuppressedUnderModal asserts that while the CONNECTION_MANAGER
// modal is top of the focus stack the rail pane does not paint (no SetView for
// the rail view). buildTestGui starts with the modal pushed.
func TestQueryRailSuppressedUnderModal(t *testing.T) {
	g, rec := buildTestGui(t)
	// Do NOT push the QueryRail — the modal is the startup top context.
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	if rec.HasSetView(context.QueryRailViewName) {
		t.Errorf("rail view %q was SetView'd under the CONNECTION_MANAGER modal; the pane must be suppressed", context.QueryRailViewName)
	}
}

// TestQueryRailNarrowPaneNoPanic feeds a pane whose width is below any
// tab-strip width: the body must render without the strip and without panic.
func TestQueryRailNarrowPaneNoPanic(t *testing.T) {
	g, _ := buildTestGui(t)
	if err := g.ContextTree().Push(g.Registry().QueryRail); err != nil {
		t.Fatalf("Push(QueryRail): %v", err)
	}
	// Just above the limit threshold so layout proceeds, but the main pane is
	// far narrower than the combined tab labels. Must not panic.
	if err := g.RunLayout(limitProbeWidth, limitProbeHeight); err != nil {
		t.Fatalf("RunLayout narrow: %v", err)
	}
}

const (
	limitProbeWidth  = 12
	limitProbeHeight = 12
)
