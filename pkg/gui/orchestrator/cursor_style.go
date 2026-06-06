package orchestrator

import (
	"github.com/gdamore/tcell/v3"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// cursorStyleForMode maps the editor's current Mode to the terminal caret
// shape, matching neovim's convention: a blinking bar while inserting and
// a steady block everywhere else (normal, visual, operator-pending, …).
// ModeInsert is a bit flag, so Has covers composite states (e.g.
// insert+replace) without an explicit case per combination.
func cursorStyleForMode(m types.Mode) tcell.CursorStyle {
	if m.Has(types.ModeInsert) {
		return tcell.CursorStyleBlinkingBar
	}
	return tcell.CursorStyleSteadyBlock
}
