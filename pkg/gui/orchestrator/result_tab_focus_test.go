package orchestrator_test

import (
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/query"
)

// Writes run only when this test drains the production UI queue.
type errorTabClipboard struct{ writes []string }

func (c *errorTabClipboard) Write(text string) error {
	c.writes = append(c.writes, text)
	return nil
}

func newErrorTabYankGui(t *testing.T) (*orchestrator.Gui, *queueingDriver, *errorTabClipboard) {
	t.Helper()
	fs := afero.NewMemMapFs()
	c := common.NewCommon(slog.New(slog.DiscardHandler), i18n.EnglishTranslationSet(), config.GetDefaultConfig(), &common.AppState{}, fs)
	g := orchestrator.NewGui(orchestrator.Deps{
		Common: c, Store: common.NewAppStateStore(fs, "/state.yml", common.DefaultClock()),
		ConnectionsProvider: func() []models.Connection { return nil },
		HistoryProvider:     func() (*query.History, error) { return nil, nil },
	}, orchestrator.WithKeyDelays(20*time.Millisecond, time.Millisecond, time.Second))
	d := newQueueingDriver()
	require.NoError(t, g.UseDriverForTest(d))
	t.Cleanup(func() { require.NoError(t, g.Close()) })
	// Only matcher expiry may enqueue during the pending-y assertion window.
	g.WhichKey().SetOnVisible(nil)
	cb := &errorTabClipboard{}
	g.Controllers().ResultTabs.SetClipboard(cb)
	require.NoError(t, g.ContextTree().Push(g.Registry().QueryRail))
	d.EnableRealView(string(types.QUERY_EDITOR))
	g.ModeStore().Set(types.QUERY_EDITOR, types.ModeNormal)
	d.drainPending()
	return g, d, cb
}

func feedErrorTabChord(t *testing.T, g *orchestrator.Gui, d *queueingDriver, shorthand string) {
	t.Helper()
	require.NoError(t, g.RunLayout(120, 40))
	seq, err := keys.SequenceFromShorthand(shorthand)
	require.NoError(t, err)
	result, err := d.FeedChord(g.ContextTree().Current().GetViewName(), seq)
	require.NoError(t, err)
	require.NotEqual(t, keys.FellThrough, result, "chord %s did not dispatch", shorthand)
}

func queuedErrorTabExpiry(t *testing.T, d *queueingDriver) func() error {
	t.Helper()
	select {
	case expiry := <-d.updates:
		return expiry
	case <-time.After(3 * time.Second):
		t.Fatal("single-y expiry did not reach production UI queue")
		return nil
	}
}

func TestErrorTabYankDispatchManualRoutes(t *testing.T) {
	for _, route := range []string{"4", "<tab>", "<c-j>"} {
		for _, chord := range []string{"y", "yy"} {
			t.Run(route+"/"+chord, func(t *testing.T) {
				g, d, cb := newErrorTabYankGui(t)
				h := g.ResultTabsHelper()
				h.ShowError("aggregation", &drivers.QueryError{
					Raw: errors.New("aggregate é\x07"), Code: "42803", Position: 1,
					Detail: strings.Repeat("diagnostic\x1b[31m\n", 30), Hint: "group by",
				})
				tab := h.Active()
				tab.SetErrorSQL("SELECT bad\nnot displayed")
				d.EnableRealView(tab.ViewName())
				d.drainPending()
				require.Same(t, g.Registry().QueryRail, g.ContextTree().Current(), "error must not auto-focus")
				feedErrorTabChord(t, g, d, route)
				require.Equal(t, tab.ViewName(), g.ContextTree().Current().GetViewName())
				require.Equal(t, types.RESULT_GRID, g.ContextTree().Current().GetKey())
				body, ok := tab.ErrorPanelText()
				require.True(t, ok)
				require.Equal(t, body, d.GetViewBuffer(tab.ViewName()))
				d.drainPending()
				feedErrorTabChord(t, g, d, "y")
				expiry := queuedErrorTabExpiry(t, d)
				require.Empty(t, cb.writes, "timer may not write before UI delivery")
				if chord == "yy" {
					// Complete yy after expiry was queued, before its UI delivery.
					feedErrorTabChord(t, g, d, "y")
					require.Equal(t, []string{body}, cb.writes)
				}
				require.NoError(t, expiry())
				d.drainPending()
				require.Equal(t, []string{body}, cb.writes, "one resolved command must write once")
				require.Equal(t, tab.ViewName(), g.ContextTree().Current().GetViewName(), "yank must not move focus")
			})
		}
	}
}

func TestErrorTabYankDispatchCancelsQueuedExpiry(t *testing.T) {
	for _, change := range []string{"focus", "tab"} {
		t.Run(change, func(t *testing.T) {
			g, d, cb := newErrorTabYankGui(t)
			h := g.ResultTabsHelper()
			h.ShowError("A", errors.New("error A"))
			a := h.Active()
			h.ShowError("B", errors.New("error B"))
			b := h.Active()
			d.EnableRealView(a.ViewName())
			d.EnableRealView(b.ViewName())
			d.drainPending()
			feedErrorTabChord(t, g, d, "4")
			d.drainPending()
			feedErrorTabChord(t, g, d, "y")
			expiry := queuedErrorTabExpiry(t, d)
			require.Empty(t, cb.writes)
			if change == "focus" {
				feedErrorTabChord(t, g, d, "3")
				require.Same(t, g.Registry().QueryRail, g.ContextTree().Current())
			} else {
				feedErrorTabChord(t, g, d, "gt")
				require.Same(t, a, h.Active())
				require.Equal(t, a.ViewName(), g.ContextTree().Current().GetViewName())
			}
			require.NoError(t, expiry())
			d.drainPending()
			require.Empty(t, cb.writes, "cancelled timeout must publish nothing")
			if change == "focus" {
				feedErrorTabChord(t, g, d, "4")
			}
			d.drainPending()
			feedErrorTabChord(t, g, d, "yy")
			body, ok := h.Active().ErrorPanelText()
			require.True(t, ok)
			require.Equal(t, []string{body}, cb.writes)
			feedErrorTabChord(t, g, d, "gt")
			feedErrorTabChord(t, g, d, "yy")
			otherBody, ok := h.Active().ErrorPanelText()
			require.True(t, ok)
			require.NotEqual(t, body, otherBody)
			require.Equal(t, []string{body, otherBody}, cb.writes)
		})
	}
}

// TestTabCycleRepointsFocusToActiveTab: with a
// grid "results" tab and a "plan analyzer" tab open, cycling between them
// while the result pane holds focus must move gocui's current-view (driven
// off tree.Current().GetViewName()) onto the now-active tab. Before the fix
// Cycle only mutated the helper's activeID, so the focus stack stayed on
// the prior tab and leader chords dispatched under the stale PLAN scope.
func TestTabCycleRepointsFocusToActiveTab(t *testing.T) {
	g, _ := buildTestGui(t)
	h := g.ResultTabsHelper()
	tree := g.ContextTree()

	if err := h.OpenResultTab("results", nil); err != nil {
		t.Fatalf("OpenResultTab: %v", err)
	}
	if err := h.OpenPlanTab("plan", models.Plan{}); err != nil {
		t.Fatalf("OpenPlanTab: %v", err)
	}
	// Enter the result pane on the (active) plan tab, as a rail switch does.
	if err := tree.Push(h.ActiveContext()); err != nil {
		t.Fatalf("Push active: %v", err)
	}
	if got := tree.Current().GetKey(); got != types.PLAN {
		t.Fatalf("precondition: focus key = %q, want PLAN", got)
	}

	// gt -> cycle to the grid tab. Focus must follow onto its view.
	h.Cycle(1)

	if got := tree.Current().GetKey(); got != types.RESULT_GRID {
		t.Fatalf("after Cycle: focus key = %q, want RESULT_GRID (stale plan scope)", got)
	}
	if got, want := tree.Current().GetViewName(), h.ActiveContext().GetViewName(); got != want {
		t.Fatalf("after Cycle: focus view = %q, want active tab view %q", got, want)
	}
}

// TestTabJumpDoesNotStealFocusWhenPaneUnfocused guards the asymmetry:
// <leader>1..9 jump bindings are GLOBAL-scoped and can fire
// from the query editor or a rail. A jump must switch the visible tab
// (activeID) but must NOT hijack the focus stack onto the result pane.
func TestTabJumpDoesNotStealFocusWhenPaneUnfocused(t *testing.T) {
	g, _ := buildTestGui(t)
	h := g.ResultTabsHelper()
	tree := g.ContextTree()

	if err := h.OpenResultTab("a", nil); err != nil {
		t.Fatalf("OpenResultTab a: %v", err)
	}
	if err := h.OpenResultTab("b", nil); err != nil {
		t.Fatalf("OpenResultTab b: %v", err)
	}

	// Focus is on whatever buildTestGui rooted (a non-result context);
	// the result pane is NOT focused.
	before := tree.Current()
	if k := before.GetKey(); k == types.RESULT_GRID || k == types.PLAN {
		t.Fatalf("precondition: root focus = %q, want a non-result context", k)
	}

	h.Jump(1) // switch visible tab from b -> a

	if got := h.Active(); got == nil || got.Label() != "a" {
		t.Fatalf("Jump did not switch active tab: %v", got)
	}
	if tree.Current() != before {
		t.Fatalf("Jump stole focus: top = %q, want unchanged %q",
			tree.Current().GetKey(), before.GetKey())
	}
}
