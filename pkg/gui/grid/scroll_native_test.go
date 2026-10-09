package grid

import (
	"errors"
	"strings"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/pgsavvy/pkg/models"
)

func TestGridNativeCellAlignment(t *testing.T) {
	for _, tc := range []struct {
		name, value, cluster string
		clusterWidth         int
	}{
		{"CJK", "界x", "界", 2},
		{"combining", "e\u0301x", "e\u0301", 1},
		{"ZWJ", "👩‍💻x", "👩‍💻", 2},
		{"modifier", "👍🏽x", "👍🏽", 2},
		{"VS16", "☺️x", "☺️", 2},
		{"VS15", "⌛︎x", "⌛︎", 2},
		{"keycap", "1️⃣x", "1️⃣", 2},
		{"flag", "🇺🇸x", "🇺🇸", 2},
	} {
		for _, query := range []string{"", "x", tc.cluster, string([]rune(tc.cluster)[0])} {
			t.Run(tc.name+"/query="+query, func(t *testing.T) {
				previous := gocui.Screen
				gui, err := gocui.NewGui(gocui.NewGuiOpts{OutputMode: gocui.OutputNormal, Headless: true, Width: 40, Height: 10})
				require.NoError(t, err)
				t.Cleanup(func() { gui.Close(); gocui.Screen = previous })
				v := makeView([]models.ColumnMeta{textCol(tc.cluster + "v"), textCol("next")}, []models.Row{
					{Values: []any{tc.value, "NEXT"}},
					{Values: []any{"A\nB\tx", "AFTER"}},
					{Values: []any{strings.Repeat("a", 5) + tc.cluster + "x", "CLIPPED"}},
				})
				v.widths = []int{6, 8}
				v.SetSearch(query)
				gui.SetManagerFunc(func(g *gocui.Gui) error {
					target, err := g.SetView("grid", 0, 0, 21, 5, 0)
					if err != nil && !errors.Is(err, gocui.ErrUnknownView) {
						return err
					}
					target.Wrap = false
					v.Render(target)
					return nil
				})
				require.NoError(t, gui.ForceLayoutAndRedraw())
				text, _, width := gocui.Screen.Get(1, 1)
				require.Equal(t, tc.cluster, text)
				require.Equal(t, tc.clusterWidth, width)
				text, _, _ = gocui.Screen.Get(1+tc.clusterWidth, 1)
				require.Equal(t, "v", text)
				text, _, _ = gocui.Screen.Get(8, 1)
				require.Equal(t, "│", text)
				require.Equal(t, 0, v.headerColumnAt(5))
				require.Equal(t, -1, v.headerColumnAt(7))
				require.Equal(t, 1, v.headerColumnAt(9))
				text, _, width = gocui.Screen.Get(1, 2)
				require.Equal(t, tc.cluster, text)
				require.Equal(t, tc.clusterWidth, width)
				text, _, _ = gocui.Screen.Get(1+tc.clusterWidth, 2)
				require.Equal(t, "x", text, "following character must not be overwritten by a wide backend cell")
				for row, want := range []string{"NEXT", "AFTER", "CLIPPED"} {
					for i, ch := range want {
						text, _, width = gocui.Screen.Get(10+i, row+2)
						require.Equal(t, string(ch), text, "next column at row %d, column %d", row+2, 10+i)
						require.Equal(t, 1, width)
					}
				}
				for x := 1; x <= 5; x++ {
					text, _, _ = gocui.Screen.Get(x, 4)
					require.Equal(t, "a", text)
				}
				text, _, width = gocui.Screen.Get(6, 4)
				require.Equal(t, "…", text)
				require.Equal(t, 1, width)
				require.Len(t, v.VisibleRows(), 3)
			})
		}
	}
}
