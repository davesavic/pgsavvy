package editor

import (
	"github.com/jesseduffield/lazygit/pkg/gocui"
)

// NaiveEditor is the multi-line, mode-less editor used by the
// QUERY_EDITOR context in v1 (dbsavvy-66p.11). It satisfies
// gocui.Editor by delegating every keystroke straight to
// gocui.DefaultEditor, which writes to the view's TextArea. There are
// no vim modes; Enter inserts a newline, Backspace deletes, arrow keys
// move the cursor — the standard DefaultEditor surface.
//
// The buffer state lives on *gocui.View.TextArea (gocui-owned). The
// helpers Buffer / Lines / Cursor / Selection take a *gocui.View so
// callers can read the buffer after the fact. Selection has no source
// of truth in v1 (visual mode lands in E9) and always returns ok=false.
//
// The zero value is a usable editor.
type NaiveEditor struct{}

// New constructs a NaiveEditor. The zero value works equally well; New
// is provided for callsite clarity.
func New() *NaiveEditor { return &NaiveEditor{} }

// Edit implements gocui.Editor by delegating to gocui.DefaultEditor.
// Returns true ("handled, do not propagate") when DefaultEditor
// recognises the key and false otherwise.
func (e *NaiveEditor) Edit(v *gocui.View, key gocui.Key) bool {
	if v == nil {
		return false
	}
	return gocui.DefaultEditor.Edit(v, key)
}

// ViewBuffer returns the full text content of v's TextArea, or "" when
// v or its TextArea is nil. Renamed from Buffer in dbsavvy-wwd.1 so the
// `editor.Buffer` identifier can be the canonical vim-buffer type (the
// shell shipped in wwd.1, filled by wwd.2). wwd.4 deletes this helper
// entirely along with NaiveEditor.
func ViewBuffer(v *gocui.View) string {
	if v == nil || v.TextArea == nil {
		return ""
	}
	return v.TextArea.GetContent()
}

// Lines returns ViewBuffer(v) split on '\n'. A nil view returns nil so
// callers can range over the empty result safely.
func Lines(v *gocui.View) []string {
	buf := ViewBuffer(v)
	if buf == "" {
		return nil
	}
	return splitLines(buf)
}

// splitLines splits on '\n', preserving empty trailing entries the
// caller may need to render. Kept private and trivial; pulled out for
// test clarity.
func splitLines(buf string) []string {
	if buf == "" {
		return nil
	}
	out := []string{}
	start := 0
	for i := 0; i < len(buf); i++ {
		if buf[i] == '\n' {
			out = append(out, buf[start:i])
			start = i + 1
		}
	}
	out = append(out, buf[start:])
	return out
}

// Cursor returns the (line, col) position of the cursor inside v's
// TextArea. Returns the zero Position when the view or its TextArea
// is nil. TextArea.GetCursorXY reports (x, y) — Cursor maps that into
// (Col, Line) so callers reason in source-order terms.
func Cursor(v *gocui.View) Position {
	if v == nil || v.TextArea == nil {
		return Position{}
	}
	x, y := v.TextArea.GetCursorXY()
	return Position{Line: y, Col: x}
}

// Selection returns (start, end, ok) for the current visual selection.
// v1 has no visual mode (vim editor lands in E9) so this always
// returns ok=false. Kept as a public surface so callers can write
// future-proof code today.
func Selection(_ *gocui.View) (Position, Position, bool) {
	return Position{}, Position{}, false
}
