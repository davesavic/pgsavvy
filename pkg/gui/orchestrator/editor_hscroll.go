package orchestrator

import "github.com/jesseduffield/lazygit/pkg/gocui"

// scrollEditorColumnIntoView pins the QUERY_EDITOR view's horizontal
// origin so the caret column stays inside the visible viewport, then
// rewrites the screen-relative cursor x.
//
// The query editor keeps Wrap=false so the vim buffer's logical
// (col,line) coordinates map 1:1 to view rows/cols. gocui's FocusPoint
// pins only the vertical origin (oy) and sets v.cx to the absolute
// column, so a line wider than InnerWidth clips past the right border
// and — because gui.go's ShowCursor guard requires cx < InnerWidth —
// the caret is hidden once col reaches the edge. This restores the
// missing horizontal axis: it scrolls ox like FocusPoint scrolls oy.
func scrollEditorColumnIntoView(v *gocui.View, col int) {
	inner := v.InnerWidth()
	if inner <= 0 {
		return
	}
	ox := horizontalOriginFor(col, v.OriginX(), inner)
	v.SetOriginX(ox)
	v.SetCursorX(col - ox)
}

// horizontalOriginFor returns the horizontal origin that keeps `col`
// visible within a `width`-column viewport currently scrolled to
// `origin`. It is the X-axis analogue of gocui's calculateNewOrigin,
// but edge-anchored (caret parks just inside the nearer edge) rather
// than centered, matching a text editor's horizontal scroll.
func horizontalOriginFor(col, origin, width int) int {
	if col < origin {
		return col
	}
	if col >= origin+width {
		return col - width + 1
	}
	return origin
}
