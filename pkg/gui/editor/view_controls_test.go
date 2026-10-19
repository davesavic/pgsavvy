package editor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/pgsavvy/pkg/theme"
)

func TestPaintBuffer_ControlsCannotConcealSQL(t *testing.T) {
	const raw = "DELETE FROM users;\rSELECT 1;\x1b[2K\x1b]52;c;secret\a\b\v\f\x00\x7f\u009b31m"
	const visible = "DELETE FROM users;␍SELECT 1;␛[2K␛]52;c;secret␇␈␋␌␀␡�31m"
	for _, mono := range []bool{false, true} {
		for _, large := range []bool{false, true} {
			t.Run(fmt.Sprintf("mono_%t/large_%t", mono, large), func(t *testing.T) {
				t.Cleanup(theme.SetMonochromeForTest(mono))
				text := raw
				if large {
					text += "\n" + strings.Repeat("SELECT 2;\n", 10000)
				}
				buf := newBufferWithText(t, text)
				buf.SetCursor(Position{Col: 18})
				buf.Selection = &Range{Start: Position{}, End: Position{Col: 6}}
				buf.SetYankFlash(Range{Start: Position{Col: 18}, End: Position{Col: 24}})
				v := gocui.NewView("controls", 0, 0, 120, 12, gocui.OutputNormal)
				for range 2 {
					PaintBuffer(v, buf)
					require.Equal(t, visible, strings.TrimRight(v.BufferLines()[0], " "))
					x, y := v.Cursor()
					require.Equal(t, 18, x)
					require.Zero(t, y)
				}
				require.Equal(t, text, buf.String(), "display sanitization must not change executable bytes")
			})
		}
	}
}
