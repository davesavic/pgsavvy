package orchestrator

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v3"
	"github.com/gdamore/tcell/v3/vt"
	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/models"
)

type modalSyncScreen struct {
	tcell.Screen
	syncs int
}

func (s *modalSyncScreen) Sync() {
	s.syncs++
	s.Screen.Sync()
}

type modalDriverWithoutCount struct{ types.GuiDriver }

func newModalTeardownGUI(t *testing.T) (*Gui, *gocui.Gui, *modalSyncScreen, vt.MockTerm) {
	t.Helper()
	ng := newCellViewerHeadlessGUI(t, 130, 42)
	screen := &modalSyncScreen{Screen: gocui.Screen}
	gocui.Screen = screen
	tty, ok := screen.Tty()
	require.True(t, ok)
	terminal, ok := tty.(vt.MockTerm)
	require.True(t, ok)
	c := common.NewCommon(slog.New(slog.DiscardHandler), i18n.EnglishTranslationSet(), config.GetDefaultConfig(), &common.AppState{}, afero.NewMemMapFs())
	g := NewGui(Deps{Common: c})
	g.toastHelp = ui.NewToastHelper(nil)
	g.driver = newGocuiDriver(ng)
	g.registry = guicontext.NewContextTree(types.ContextTreeDeps{GuiDriver: g.driver})
	g.registry.ConnectionManager.SetItems([]any{models.Connection{Name: "MODAL_REMNANT_SENTINEL"}})
	ng.SetManager(g)
	return g, ng, screen, terminal
}

func modalDisplayedRegion(terminal vt.MockTerm, x0, y0, x1, y1 int) string {
	var text strings.Builder
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			text.WriteString(terminal.GetCell(vt.Coord{X: vt.Col(x), Y: vt.Row(y)}).C)
		}
		text.WriteByte('\n')
	}
	return text.String()
}

func TestRunLayoutModalRemovalSyncsWithoutCountDecrease(t *testing.T) {
	for _, modal := range []types.ContextKey{types.CONNECTION_MANAGER, types.SETTINGS} {
		for _, transition := range []string{"increased", "equal", "without-count"} {
			t.Run(string(modal)+"/"+transition, func(t *testing.T) {
				g, ng, screen, terminal := newModalTeardownGUI(t)
				if transition == "without-count" {
					g.driver = &modalDriverWithoutCount{GuiDriver: g.driver}
				}
				if transition == "equal" {
					require.NoError(t, g.tree.Push(g.registry.SchemaRail))
					require.NoError(t, ng.ForceLayoutAndRedraw())
				}
				ctx := g.registry.ByKey(modal)
				require.NoError(t, g.tree.Push(ctx))
				require.NoError(t, ng.ForceLayoutAndRedraw())
				require.NoError(t, terminal.Drain())
				view, err := ng.View(ctx.GetViewName())
				require.NoError(t, err)
				x0, y0, x1, y1 := view.Dimensions()
				initial := modalDisplayedRegion(terminal, x0, y0, x1, y1)
				require.Contains(t, initial, "│")
				if modal == types.CONNECTION_MANAGER {
					require.Contains(t, initial, "MODAL_REMNANT_SENTINEL")
				}
				before := len(ng.Views())
				if transition == "equal" {
					// Offset modal removal without changing the previous frame's count.
					replacement, err := ng.SetView("replacement", 0, 0, 1, 1, 0)
					require.ErrorIs(t, err, gocui.ErrUnknownView)
					replacement.Frame = false
				}
				screen.syncs = 0
				require.NoError(t, g.tree.Push(g.registry.SchemaRail))
				require.NoError(t, ng.ForceLayoutAndRedraw())
				if transition == "equal" {
					require.Equal(t, before, len(ng.Views()))
				} else {
					require.Greater(t, len(ng.Views()), before)
				}
				require.Equal(t, 1, screen.syncs, "successful modal removal must force one refresh")
				_, err = ng.View(ctx.GetViewName())
				require.ErrorIs(t, err, gocui.ErrUnknownView)
				_, err = ng.View(g.registry.QueryRail.GetViewName())
				require.NoError(t, err)
				_, err = ng.View(ResultEmptyViewName)
				require.NoError(t, err)
				require.NoError(t, terminal.Drain())
				actual := modalDisplayedRegion(terminal, x0, y0, x1, y1)
				require.NotContains(t, actual, "MODAL_REMNANT_SENTINEL")
				// Compare displayed cells, not just the screen's logical back buffer.
				screen.Screen.Sync()
				require.NoError(t, terminal.Drain())
				require.Equal(t, modalDisplayedRegion(terminal, x0, y0, x1, y1), actual)
				require.NoError(t, ng.ForceLayoutAndRedraw())
				require.Equal(t, 1, screen.syncs, "already-absent modals must not refresh again")
			})
		}
	}
}

func TestRunLayoutViewCountDecreaseStillSyncs(t *testing.T) {
	g, ng, screen, _ := newModalTeardownGUI(t)
	require.NoError(t, g.tree.Push(g.registry.SchemaRail))
	require.NoError(t, ng.ForceLayoutAndRedraw())
	view, err := ng.SetView("unrelated-popup", 0, 0, 1, 1, 0)
	require.ErrorIs(t, err, gocui.ErrUnknownView)
	view.Frame = false
	require.NoError(t, ng.ForceLayoutAndRedraw())
	screen.syncs = 0
	require.NoError(t, ng.DeleteView("unrelated-popup"))
	require.NoError(t, ng.ForceLayoutAndRedraw())
	require.Equal(t, 1, screen.syncs)
	require.NoError(t, ng.ForceLayoutAndRedraw())
	require.Equal(t, 1, screen.syncs)
}

func TestRunLayoutModalReplacementSyncsOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode guicontext.ConnectionManagerMode
	}{{"connecting", guicontext.ModeConnecting}, {"form", guicontext.ModeForm}} {
		t.Run(tc.name, func(t *testing.T) {
			g, ng, screen, _ := newModalTeardownGUI(t)
			require.NoError(t, g.tree.Push(g.registry.Settings))
			require.NoError(t, ng.ForceLayoutAndRedraw())
			if tc.mode == guicontext.ModeForm {
				g.registry.ConnectionManager.OpenAddForm(nil, func() []string { return []string{"postgres"} })
			} else {
				g.registry.ConnectionManager.SetMode(tc.mode)
			}
			screen.syncs = 0
			require.NoError(t, g.tree.Push(g.registry.ConnectionManager))
			require.NoError(t, ng.ForceLayoutAndRedraw())
			require.Equal(t, 1, screen.syncs, "teardown and changed modal body must share one refresh")
			require.NotEmpty(t, g.prevModalBody)
			require.NoError(t, ng.ForceLayoutAndRedraw())
			require.Equal(t, 1, screen.syncs, "the modal body snapshot must advance even when refreshes are coalesced")
		})
	}
}
