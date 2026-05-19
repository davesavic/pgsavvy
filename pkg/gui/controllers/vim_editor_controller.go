package controllers

import (
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/editor"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// VimEditorController owns the motion / operator / textobject
// keybindings under the QUERY_EDITOR scope (epic dbsavvy-wwd).
//
// Unlike the side-rail controllers, VimEditorController is NOT built
// on baseController. It takes explicit dependencies (qec, matcher)
// so the wiring is unambiguous: the controller closes over a single
// *editor.Buffer (the one inside the live QueryEditorContext) and
// reaches the keybinding Matcher directly when it needs to (e.g.
// applyPending in wwd.8 will register an operator-pending state in
// the Matcher rather than mutating the buffer immediately).
//
// wwd.5 ships the motion layer ONLY. Bindings are registered under
// Normal + OperatorPending. Visual modes are added in wwd.7; operator
// dispatch is added in wwd.8.
type VimEditorController struct {
	qec     *context.QueryEditorContext
	matcher *keys.Matcher
}

// NewVimEditorController constructs the controller. Either argument
// may be nil — the controller silently no-ops when its buffer or
// matcher is missing. The orchestrator wires concrete values; tests
// may pass nil to exercise the GetKeybindings / RegisterActions
// surface independently.
func NewVimEditorController(qec *context.QueryEditorContext, matcher *keys.Matcher) *VimEditorController {
	return &VimEditorController{qec: qec, matcher: matcher}
}

// motionFunc is the shared signature every motion in pkg/gui/editor
// implements: pure (no Buffer mutation), pre-validated by the caller.
type motionFunc func(b *editor.Buffer, pos editor.Position, count int) (editor.Position, bool)

// textObjectFunc resolves a Range from a cursor position. Bool=false
// means "no surrounding object found" — the handler MUST NOT call
// applyPending in that case.
type textObjectFunc func(b *editor.Buffer, pos editor.Position) (editor.Range, bool)

// textObjectSpec ties together a default key shorthand, an action ID,
// a human description, and the resolver. wwd.6 registers bindings
// under OperatorPending only; wwd.7 extends the mode mask to include
// Visual / VisualLine once those mode primitives ship.
type textObjectSpec struct {
	shorthand   string
	actionID    string
	description string
	fn          textObjectFunc
}

// motionSpec ties together a default key shorthand, an action ID, a
// human description, and the pure motion function the handler invokes.
// jump = true classifies the motion for JumpList recording (gg, G,
// {, }, mark_jump per the wwd architecture decisions).
type motionSpec struct {
	shorthand   string
	actionID    string
	description string
	fn          motionFunc
	jump        bool
}

func (c *VimEditorController) motionSpecs() []motionSpec {
	return []motionSpec{
		// Character motions.
		{"h", commands.MotionCharLeft, "char left", editor.CharLeft, false},
		{"l", commands.MotionCharRight, "char right", editor.CharRight, false},
		{"j", commands.MotionLineDown, "line down", editor.LineDown, false},
		{"k", commands.MotionLineUp, "line up", editor.LineUp, false},

		// Word motions.
		{"w", commands.MotionWordNext, "word forward", editor.WordNext, false},
		{"b", commands.MotionWordPrev, "word back", editor.WordPrev, false},
		{"e", commands.MotionWordEnd, "word end", editor.WordEnd, false},
		{"W", commands.MotionWordNextBig, "WORD forward", editor.WORDNext, false},
		{"B", commands.MotionWordPrevBig, "WORD back", editor.WORDPrev, false},
		{"E", commands.MotionWordEndBig, "WORD end", editor.WORDEnd, false},

		// Line motions.
		{"0", commands.MotionLineStart, "line start", editor.LineStart, false},
		{"^", commands.MotionLineFirstNonblank, "first non-blank", editor.LineFirstNonBlank, false},
		{"$", commands.MotionLineEnd, "line end", editor.LineEnd, false},

		// Buffer motions (jumps).
		{"gg", commands.MotionBufferStart, "buffer start", editor.BufferStart, true},
		{"G", commands.MotionBufferEnd, "buffer end", editor.BufferEnd, true},

		// Paragraph + sentence motions (paragraphs are jumps).
		{"{", commands.MotionParagraphPrev, "paragraph prev", editor.ParagraphPrev, true},
		{"}", commands.MotionParagraphNext, "paragraph next", editor.ParagraphNext, true},
		{"(", commands.MotionSentencePrev, "sentence prev", editor.SentencePrev, false},
		{")", commands.MotionSentenceNext, "sentence next", editor.SentenceNext, false},

		// Screen-relative motions (buffer-relative stubs in wwd.5).
		{"H", commands.MotionScreenTop, "screen top", editor.ScreenTop, false},
		{"M", commands.MotionScreenMiddle, "screen middle", editor.ScreenMiddle, false},
		{"L", commands.MotionScreenBottom, "screen bottom", editor.ScreenBottom, false},
	}
}

// GetKeybindings publishes the motion bindings under QUERY_EDITOR
// scope. Mode = Normal | OperatorPending — Visual variants are added
// in wwd.7. The mark-jump binding is NOT published here; the `'a..z`
// recall family is shipped by wwd.7 alongside the visual extensions.
//
// Text-object bindings (i"/a", i(/a(, ip/ap, is/as, …) are appended
// under OperatorPending only — wwd.7 extends the mode mask to
// Visual / VisualLine once those modes are wired.
func (c *VimEditorController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	specs := c.motionSpecs()
	textObjects := c.textObjectSpecs()
	out := make([]*types.ChordBinding, 0, len(specs)+len(textObjects))
	for _, s := range specs {
		seq, err := keys.SequenceFromShorthand(s.shorthand)
		if err != nil {
			continue
		}
		out = append(out, &types.ChordBinding{
			Sequence:    seq,
			Mode:        types.ModeNormal | types.ModeOperatorPending,
			Scope:       types.QUERY_EDITOR,
			ActionID:    s.actionID,
			Description: s.description,
			Tag:         "Motion",
		})
	}
	for _, s := range textObjects {
		seq, err := keys.SequenceFromShorthand(s.shorthand)
		if err != nil {
			continue
		}
		out = append(out, &types.ChordBinding{
			Sequence:    seq,
			Mode:        types.ModeOperatorPending,
			Scope:       types.QUERY_EDITOR,
			ActionID:    s.actionID,
			Description: s.description,
			Tag:         "Text object",
		})
	}
	return out
}

// RegisterActions wires each motion action ID to a handler that:
//
//  1. normalises ExecCtx.Count (0 → 1, <0 → no-op),
//  2. resolves the new Position via the pure motion func,
//  3. dispatches:
//     - operator-pending → applyPending(buf, Range{cursor, newPos})
//     - else → set Cursor; push jump (when motion is classed as jump)
//
// applyPending is a stub in wwd.5 (returns nil); wwd.8 fills the body.
func (c *VimEditorController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	for _, s := range c.motionSpecs() {
		spec := s
		_ = reg.Register(&commands.Command{
			ID:          spec.actionID,
			Description: spec.description,
			Tag:         "Motion",
			Handler:     c.motionHandler(spec),
		})
	}
	// iB/i{ and aB/a{ share action IDs (vim alias) — register handler once.
	seen := make(map[string]struct{})
	for _, s := range c.textObjectSpecs() {
		spec := s
		if _, ok := seen[spec.actionID]; ok {
			continue
		}
		seen[spec.actionID] = struct{}{}
		_ = reg.Register(&commands.Command{
			ID:          spec.actionID,
			Description: spec.description,
			Tag:         "Text object",
			Handler:     c.textObjectHandler(spec),
		})
	}
}

// textObjectSpecs returns the wwd.6 text-object table. Quote-text
// objects use a small adapter to bind the quote rune into a
// textObjectFunc closure.
func (c *VimEditorController) textObjectSpecs() []textObjectSpec {
	innerQuote := func(q rune) textObjectFunc {
		return func(b *editor.Buffer, pos editor.Position) (editor.Range, bool) {
			return editor.InnerQuote(b, pos, q)
		}
	}
	aroundQuote := func(q rune) textObjectFunc {
		return func(b *editor.Buffer, pos editor.Position) (editor.Range, bool) {
			return editor.AroundQuote(b, pos, q)
		}
	}
	return []textObjectSpec{
		{"i\"", commands.TextObjectInnerQuoteDouble, "inside \"", innerQuote('"')},
		{"a\"", commands.TextObjectAroundQuoteDouble, "around \"", aroundQuote('"')},
		{"i'", commands.TextObjectInnerQuoteSingle, "inside '", innerQuote('\'')},
		{"a'", commands.TextObjectAroundQuoteSingle, "around '", aroundQuote('\'')},
		{"i(", commands.TextObjectInnerParen, "inside ()", editor.InnerParen},
		{"a(", commands.TextObjectAroundParen, "around ()", editor.AroundParen},
		{"i[", commands.TextObjectInnerBracket, "inside []", editor.InnerBracket},
		{"a[", commands.TextObjectAroundBracket, "around []", editor.AroundBracket},
		{"i{", commands.TextObjectInnerBrace, "inside {}", editor.InnerBraces},
		{"a{", commands.TextObjectAroundBrace, "around {}", editor.AroundBraces},
		{"iB", commands.TextObjectInnerBrace, "inside {} (iB)", editor.InnerBraces},
		{"aB", commands.TextObjectAroundBrace, "around {} (aB)", editor.AroundBraces},
		{"ip", commands.TextObjectInnerParagraph, "inside paragraph", editor.InnerParagraph},
		{"ap", commands.TextObjectAroundParagraph, "around paragraph", editor.AroundParagraph},
		{"is", commands.TextObjectInnerStatement, "inside statement", editor.InnerStatement},
		{"as", commands.TextObjectAroundStatement, "around statement", editor.AroundStatement},
	}
}

// textObjectHandler returns the Handler closure for one textObjectSpec.
// When fired in OperatorPending mode, it resolves the range and hands
// off to applyPending (stub in wwd.5; filled in wwd.8). Outside
// OperatorPending the handler is a no-op (Visual handling lands in
// wwd.7).
func (c *VimEditorController) textObjectHandler(spec textObjectSpec) commands.Handler {
	return func(ec commands.ExecCtx) error {
		if !ec.Mode.Has(types.ModeOperatorPending) {
			return nil
		}
		buf := c.buffer()
		if buf == nil {
			return nil
		}
		from := buf.CursorPos()
		r, ok := spec.fn(buf, from)
		if !ok {
			return nil
		}
		return c.applyPending(buf, r)
	}
}

// motionHandler returns the Handler closure for one motionSpec.
func (c *VimEditorController) motionHandler(spec motionSpec) commands.Handler {
	return func(ec commands.ExecCtx) error {
		buf := c.buffer()
		if buf == nil {
			return nil
		}
		count := ec.Count
		if count == 0 {
			count = 1
		}
		if count < 0 {
			return nil
		}
		from := buf.CursorPos()
		newPos, ok := spec.fn(buf, from, count)
		if !ok {
			return nil
		}
		if ec.Mode.Has(types.ModeOperatorPending) {
			return c.applyPending(buf, editor.Range{Start: from, End: newPos})
		}
		buf.SetCursor(newPos)
		if spec.jump && buf.Jumps != nil {
			buf.Jumps.Push(from)
		}
		return nil
	}
}

// applyPending is the operator-dispatch stub. wwd.5 records the
// pending intent only in its docstring; wwd.8 fills the body with
// the operator registry lookup + Range application. Returning nil
// here means the Buffer.Cursor is intentionally NOT moved — the
// operator handler in wwd.8 is responsible for both the edit AND
// the post-edit cursor position.
func (c *VimEditorController) applyPending(_ *editor.Buffer, _ editor.Range) error {
	// wwd.8 fills: resolve pending operator from Matcher state,
	// look up operator handler in commands.Registry, call
	// handler with the supplied Range, clear operator-pending mode.
	return nil
}

// buffer returns the live *editor.Buffer of the wired
// QueryEditorContext, or nil when qec is nil (test wiring).
func (c *VimEditorController) buffer() *editor.Buffer {
	if c.qec == nil {
		return nil
	}
	return c.qec.Buffer()
}
