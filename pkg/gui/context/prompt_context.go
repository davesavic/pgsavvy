package context

import (
	"strings"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// PromptState is the renderer-facing surface PromptContext.HandleRender
// reads each frame. *ui.PromptHelper supplies the label + active flag;
// the typed buffer lives on the underlying gocui View's TextArea (the
// PROMPT view is editable per dbsavvy-fq9 so paste / arrow-key editing
// from gocui.DefaultEditor lands directly there).
type PromptState interface {
	Active() bool
	Label() string
}

// PromptContext renders the single-line prompt popup.
//
// Runtime source of truth for the typed input is the view's TextArea:
// the PROMPT view is marked Editable in the layout pass and a master
// gocui.Editor is installed (NewMasterEditor) so dispatched runes flow
// through gocui.DefaultEditor — which handles printable runes,
// Backspace, Delete, Left/Right/Home/End and bracketed paste. The
// orchestrator plumbs the live *gocui.View handle in via SetView each
// Layout frame; ReadAndClearBuffer pulls the typed text from
// v.TextArea.GetContent and clears it. The buf field below remains as
// a test-only seam: SetBuffer is used by unit tests that don't wire a
// real view.
type PromptContext struct {
	BaseContext

	deps       Deps
	modes      types.ModeSetter
	state      PromptState
	buf        string
	view       types.View
	labelWrap  int
	labelLines int
	masked     bool
}

// secretMaskRune is rendered in place of every typed character while the
// prompt is in masked (secret) mode. renderBuffer substitutes it for every
// grapheme of the real buffer in the content body so the real value never
// reaches the screen.
const secretMaskRune = "•"

// SetMasked toggles masked (secret) rendering. While masked, HandleRender
// substitutes secretMaskRune for every buffer character (see renderBuffer) so
// the typed value never enters the view's content buffer and is never drawn.
// The real typed value always stays in the TextArea, read verbatim by
// ReadAndClearBuffer.
//
// We deliberately do NOT set the live gocui View.Mask: it masks EVERY cell of
// the view at draw time (gocui view.go setCharacter), which would also mask the
// prompt label that HandleRender writes into the same view content — rendering
// the whole popup as bullets (dbsavvy-3ye.8). Content-level masking in
// renderBuffer is sufficient because SetContent is the last writer to the
// view's line buffer before every draw (the layout pass runs HandleRender ->
// SetContent before gocui's draw, overwriting any plaintext the live TextArea
// editor echoed into the buffer).
func (p *PromptContext) SetMasked(on bool) { p.masked = on }

// Masked reports whether the prompt is in masked (secret) mode. The
// layout pass reads it to size the popup compactly: a masked prompt has
// no in-body label (it's the title), so its body is a single input line.
func (p *PromptContext) Masked() bool { return p.masked }

// GetTitle renders the prompt label as the frame heading while masked.
// The masked prompt is the SSH credential prompt (the only caller of
// SetMasked), so its label ("SSH Password" / "SSH Key Password") moves
// to the box title and HandleRender drops the in-body label line.
// Unmasked prompts fall back to the BaseContext title (empty by default,
// which suppresses the heading) so their appearance is unchanged.
func (p *PromptContext) GetTitle() string {
	if p.masked && p.state != nil {
		return p.state.Label()
	}
	return p.BaseContext.GetTitle()
}

// NewPromptContext builds a PromptContext bound to PROMPT. The state
// reader is wired post-construction via SetState. modes may be nil
// (test wiring) — HandleFocus / HandleFocusLost become no-ops in that
// case. Mode wiring matches CommandLineContext: PROMPT focus sets
// ModeCommand so the master Editor's Passthrough delegates printable
// runes through gocui.DefaultEditor to the TextArea.
func NewPromptContext(base BaseContext, deps Deps) *PromptContext {
	return &PromptContext{BaseContext: base, deps: deps}
}

// SetModes records the ModeSetter the focus hooks should toggle. Wired
// post-construction so the test factory can leave modes nil. Mirrors
// the m-setter ctor argument on CommandLineContext but kept as a
// setter so the existing constructor signature (used by many sites)
// stays source-compatible.
func (p *PromptContext) SetModes(m types.ModeSetter) { p.modes = m }

// HandleFocus sets the PROMPT scope mode to ModeCommand so the master
// Editor's Passthrough branch routes printable runes (and arrow / paste
// / Backspace dispatches with no chord-trie binding) through
// gocui.DefaultEditor into v.TextArea. Mirrors
// CommandLineContext.HandleFocus.
func (p *PromptContext) HandleFocus(_ types.OnFocusOpts) error {
	if p.modes != nil {
		p.modes.Set(types.PROMPT, types.ModeCommand)
	}
	return nil
}

// HandleFocusLost clears the per-scope mode and drops the cached view
// pointer. The orchestrator DeleteView's the PROMPT view on pop and
// recreates it on re-Push, so any cached pointer would dangle until
// the next Layout frame re-plumbs it.
func (p *PromptContext) HandleFocusLost(_ types.OnFocusLostOpts) error {
	if p.modes != nil {
		p.modes.Reset(types.PROMPT)
	}
	p.buf = ""
	p.view = nil
	return nil
}

// SetState installs the state reader. Nil-safe: HandleRender no-ops when
// no state is set.
func (p *PromptContext) SetState(s PromptState) { p.state = s }

// LabelText returns the current prompt label (empty when no state is
// wired). The layout pass reads it to size the popup wide enough that
// the label and input fit without horizontal scrolling (dbsavvy-lcxe).
func (p *PromptContext) LabelText() string {
	if p.state == nil {
		return ""
	}
	return p.state.Label()
}

// SetView is called by the orchestrator's Layout Tier-3 popup pass each
// frame the PROMPT is on the focus stack. ReadAndClearBuffer reads
// typed text from the supplied view's TextArea. Masking is handled entirely
// at the content level by renderBuffer (see SetMasked), so SetView does not
// touch the live View.Mask.
func (p *PromptContext) SetView(v types.View) { p.view = v }

// SetBuffer replaces the test-mode typed buffer. Real runtime uses
// v.TextArea via SetView. Retained so existing unit tests (which don't
// wire a view) keep compiling.
func (p *PromptContext) SetBuffer(s string) { p.buf = s }

// Buffer returns the current typed input. Reads from v.TextArea when
// a view has been plumbed in; otherwise returns the test-mode buf.
func (p *PromptContext) Buffer() string {
	if p.view != nil && p.view.TextArea != nil {
		return p.view.TextArea.GetContent()
	}
	return p.buf
}

// ReadAndClearBuffer returns the typed text and resets it to "". Used
// by prompt.submit to atomically consume the line before the helper
// pops the popup. When a view is plumbed in, the TextArea is the
// source of truth; otherwise the test-mode buf is used.
func (p *PromptContext) ReadAndClearBuffer() string {
	if p.view != nil && p.view.TextArea != nil {
		s := p.view.TextArea.GetContent()
		p.view.TextArea.Clear()
		return s
	}
	s := p.buf
	p.buf = ""
	return s
}

// HandleRender writes the popup body — label (possibly multi-line,
// word-wrapped) on the first lines, the typed buffer with a "> "
// prefix on the line below a blank separator. No-op when no state is
// wired or when the helper reports inactive (the popup is on the focus
// stack but the helper hasn't been told what to prompt for yet).
//
// The layout pass passes the live view width into the buffer line via
// SetViewCursor; HandleRender uses LabelWrapWidth() (set by the layout
// pass) to wrap the label so validator-error labels don't truncate at
// the right edge (dbsavvy-8p5).
func (p *PromptContext) HandleRender() error {
	if p.state == nil || !p.state.Active() {
		return nil
	}
	viewName := p.GetViewName()
	// Masked (SSH credential) prompt: the label is the frame title (see
	// GetTitle), so the body is just the masked input line on row 0 — no
	// in-body label, no separator.
	if p.masked {
		p.labelLines = 0
		body := "> " + p.renderBuffer()
		writeView(p.deps, func() error {
			return p.deps.GuiDriver.SetContent(viewName, body)
		})
		return nil
	}
	wrapped := wrapLabel(p.state.Label(), p.LabelWrapWidth())
	p.labelLines = len(wrapped)
	body := assemblePromptBody(wrapped, p.renderBuffer())
	writeView(p.deps, func() error {
		return p.deps.GuiDriver.SetContent(viewName, body)
	})
	return nil
}

// LabelWrapWidth returns the column count HandleRender wraps the label
// to. The layout pass calls SetLabelWrapWidth each frame with the live
// view's InnerWidth so the wrapper tracks terminal resizes. A zero or
// negative width disables wrapping (the body is rendered verbatim).
func (p *PromptContext) LabelWrapWidth() int { return p.labelWrap }

// SetLabelWrapWidth records the wrap width the next HandleRender uses.
func (p *PromptContext) SetLabelWrapWidth(w int) { p.labelWrap = w }

// CursorXY returns the (x, y) coordinates the layout pass should pass
// to GuiDriver.SetViewCursor to anchor the visible caret. The body
// format emitted by HandleRender (via assemblePromptBody) is:
//
//	line 0..n-1: <wrapped label lines>
//	line n:     (blank)
//	line n+1:   "> " + <buffer prefix up to cursor>
//
// where n = number of label lines. The layout pass calls
// SetLabelLineCount each frame with that count, so CursorXY can return
// the correct Y. Inside line n+1 the caret X is 2 (the "> " prefix)
// plus the TextArea's intra-buffer cursor X (so Left/Right within the
// typed buffer move the caret correctly).
// ok is false when no state is wired or the prompt is inactive —
// callers must skip SetViewCursor in that case so the caret doesn't
// land on a popup that isn't visible.
func (p *PromptContext) CursorXY() (int, int, bool) {
	if p.state == nil || !p.state.Active() {
		return 0, 0, false
	}
	cx := 0
	if p.view != nil && p.view.TextArea != nil {
		cx, _ = p.view.TextArea.GetCursorXY()
	} else {
		cx = len(p.Buffer())
	}
	y := p.labelLines + 1 // <labelLines>\n + 1 blank line, then buffer
	if p.masked {
		y = 0 // masked body is the input line alone, on row 0
	}
	return 2 + cx, y, true
}

// SetLabelLineCount records the number of label lines the last
// HandleRender emitted. CursorXY uses it to compute the buffer
// row. The layout pass calls this after rendering each frame.
func (p *PromptContext) SetLabelLineCount(n int) { p.labelLines = n }

// renderBuffer returns the buffer text to write into the content. In masked
// mode every grapheme of the real buffer is replaced with secretMaskRune so
// the secret never enters the content buffer; otherwise the buffer is returned
// verbatim.
func (p *PromptContext) renderBuffer() string {
	buf := p.Buffer()
	if !p.masked {
		return buf
	}
	return strings.Repeat(secretMaskRune, len([]rune(buf)))
}

// assemblePromptBody joins pre-wrapped label lines with a blank
// separator and the "> " buffer prefix.
func assemblePromptBody(wrappedLabel []string, buffer string) string {
	var b strings.Builder
	for i, line := range wrappedLabel {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	b.WriteString("\n\n> ")
	b.WriteString(buffer)
	return b.String()
}

// wrapLabel returns label split on existing newlines, then each
// sub-line word-wrapped to width. width <= 0 disables wrapping (each
// sub-line returned verbatim). Words longer than width are not broken
// (returned as-is on their own line) to keep the implementation
// dependency-free and predictable.
func wrapLabel(label string, width int) []string {
	if label == "" {
		return []string{""}
	}
	lines := strings.Split(label, "\n")
	if width <= 0 {
		return lines
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			out = append(out, "")
			continue
		}
		words := strings.Fields(line)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		cur := words[0]
		for _, w := range words[1:] {
			if len(cur)+1+len(w) <= width {
				cur += " " + w
				continue
			}
			out = append(out, cur)
			cur = w
		}
		out = append(out, cur)
	}
	return out
}
