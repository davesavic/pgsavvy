package orchestrator

import (
	"testing"

	"github.com/gdamore/tcell/v3"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// cursorStyleForMode is the pure mode→shape mapping that drives the
// focused QUERY_EDITOR caret. Insert is the only mode that diverges from
// the steady block (neovim parity: blinking bar while typing).
func TestCursorStyleForMode(t *testing.T) {
	tests := []struct {
		name string
		mode types.Mode
		want tcell.CursorStyle
	}{
		{"normal is steady block", types.ModeNormal, tcell.CursorStyleSteadyBlock},
		{"insert is blinking bar", types.ModeInsert, tcell.CursorStyleBlinkingBar},
		{"visual stays steady block", types.ModeVisual, tcell.CursorStyleSteadyBlock},
		{"visual-line stays steady block", types.ModeVisualLine, tcell.CursorStyleSteadyBlock},
		{"operator-pending stays steady block", types.ModeOperatorPending, tcell.CursorStyleSteadyBlock},
		{"composite insert+replace still bar", types.ModeInsert | types.ModeReplace, tcell.CursorStyleBlinkingBar},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cursorStyleForMode(tt.mode); got != tt.want {
				t.Errorf("cursorStyleForMode(%s) = %v, want %v", tt.mode, got, tt.want)
			}
		})
	}
}
