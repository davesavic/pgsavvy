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

// motionModeMask is the Mode mask under which motion bindings fire.
// Visual / VisualLine / VisualBlock are added in wwd.7 so motion keys
// (h/j/k/l/w/b/...) extend the live Selection instead of just moving
// the cursor.
const motionModeMask = types.ModeNormal | types.ModeOperatorPending |
	types.ModeVisual | types.ModeVisualLine | types.ModeVisualBlock

// textObjectModeMask is the Mode mask under which text-object bindings
// fire. OperatorPending is the original (wwd.6); wwd.7 extends to
// Visual / VisualLine so `vi"`-style flows snap the Selection to the
// resolved object. ModeVisualBlock is excluded — a vim corner case
// left out of MVP.
const textObjectModeMask = types.ModeOperatorPending |
	types.ModeVisual | types.ModeVisualLine

// visualEntryModeMask is the Mode mask under which the visual-enter
// bindings (v / V / <c-v>) fire — Normal only. Re-pressing `v` in
// Visual mode is vim's "exit" gesture; wwd.7 ships `<esc>` for that
// instead, leaving v/V/<c-v> as Normal-only entry chords.
const visualEntryModeMask = types.ModeNormal

// visualExitModeMask is the Mode mask under which `<esc>` fires the
// visual.exit action — every Visual variant.
const visualExitModeMask = types.ModeVisual | types.ModeVisualLine | types.ModeVisualBlock

// GetKeybindings publishes the motion + text-object + visual bindings
// under QUERY_EDITOR scope.
//
//   - Motion bindings: motionModeMask (Normal | OperatorPending | every
//     Visual variant). In Visual mode the motion handler extends
//     Selection instead of moving the cursor; jumps are NOT pushed.
//   - Text-object bindings: textObjectModeMask (OperatorPending |
//     Visual | VisualLine).
//   - Visual-enter bindings (v / V / <c-v>): Normal only.
//   - Visual-exit binding (<esc>): every Visual variant.
//
// The mark-jump binding (`'a..z`) is NOT published here; that recall
// family ships in a later wwd task.
func (c *VimEditorController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	specs := c.motionSpecs()
	textObjects := c.textObjectSpecs()
	visuals := c.visualSpecs()
	out := make([]*types.ChordBinding, 0, len(specs)+len(textObjects)+len(visuals))
	for _, s := range specs {
		seq, err := keys.SequenceFromShorthand(s.shorthand)
		if err != nil {
			continue
		}
		out = append(out, &types.ChordBinding{
			Sequence:    seq,
			Mode:        motionModeMask,
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
			Mode:        textObjectModeMask,
			Scope:       types.QUERY_EDITOR,
			ActionID:    s.actionID,
			Description: s.description,
			Tag:         "Text object",
		})
	}
	for _, s := range visuals {
		seq, err := keys.SequenceFromShorthand(s.shorthand)
		if err != nil {
			continue
		}
		out = append(out, &types.ChordBinding{
			Sequence:    seq,
			Mode:        s.mode,
			Scope:       types.QUERY_EDITOR,
			ActionID:    s.actionID,
			Description: s.description,
			Tag:         "Visual",
		})
	}
	return out
}

// visualSpec ties a shorthand to a visual action ID and its mode mask.
// Entry chords (v / V / <c-v>) carry visualEntryModeMask; the exit
// chord (<esc>) carries visualExitModeMask.
type visualSpec struct {
	shorthand   string
	actionID    string
	description string
	mode        types.Mode
}

// visualSpecs returns the wwd.7 visual-entry + visual-exit table.
func (c *VimEditorController) visualSpecs() []visualSpec {
	return []visualSpec{
		{"v", commands.VisualEnter, "enter visual", visualEntryModeMask},
		{"V", commands.VisualEnterLine, "enter visual-line", visualEntryModeMask},
		{"<c-v>", commands.VisualEnterBlock, "enter visual-block", visualEntryModeMask},
		{"<esc>", commands.VisualExit, "exit visual", visualExitModeMask},
	}
}

// RegisterActions wires each motion action ID to a handler that:
//
//  1. normalises ExecCtx.Count (0 → 1, <0 → no-op),
//  2. resolves the new Position via the pure motion func,
//  3. dispatches:
//     - operator-pending → applyPending(buf, Range{cursor, newPos})
//     - visual           → ExtendSelection(buf, newPos) (jumps NOT pushed)
//     - else             → set Cursor; push jump (when motion is jump)
//
// applyPending is a stub in wwd.5 (returns nil); wwd.8 fills the body.
//
// Text-object handlers in OperatorPending hand off to applyPending; in
// Visual / VisualLine they snap Buffer.Selection to the resolved range.
//
// Visual entry / exit handlers (v / V / <c-v> / <esc>) drive
// editor.EnterVisual / ExitVisual + flip QueryEditorContext mode.
// SelectionExtend is registered so audits see it; the motion handlers
// own the actual extension behaviour.
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
	// Visual entry / exit handlers (wwd.7). v / V / <c-v> share a
	// parameterised entry handler; <esc> drives the exit.
	_ = reg.Register(&commands.Command{
		ID:          commands.VisualEnter,
		Description: "Enter visual mode",
		Tag:         "Visual",
		Handler:     c.visualEnterHandler(types.ModeVisual),
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.VisualEnterLine,
		Description: "Enter visual-line mode",
		Tag:         "Visual",
		Handler:     c.visualEnterHandler(types.ModeVisualLine),
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.VisualEnterBlock,
		Description: "Enter visual-block mode",
		Tag:         "Visual",
		Handler:     c.visualEnterHandler(types.ModeVisualBlock),
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.VisualExit,
		Description: "Exit visual mode",
		Tag:         "Visual",
		Handler:     c.visualExitHandler(),
	})
	// SelectionExtend has no default chord — motion handlers own the
	// extension behaviour — but the ID is still in the registry so
	// /completeness audits and the cheatsheet can surface it.
	_ = reg.Register(&commands.Command{
		ID:          commands.SelectionExtend,
		Description: "Extend visual selection (motion-driven)",
		Tag:         "Visual",
		Handler:     commands.NopSentinel,
	})
}

// visualEnterHandler returns the Handler closure for v / V / <c-v>.
// Seeds Selection at the live Cursor via editor.EnterVisual and flips
// QUERY_EDITOR mode to the requested Visual variant.
func (c *VimEditorController) visualEnterHandler(mode types.Mode) commands.Handler {
	return func(_ commands.ExecCtx) error {
		buf := c.buffer()
		if buf == nil {
			return nil
		}
		editor.EnterVisual(buf, mode)
		if c.qec != nil {
			c.qec.SetMode(mode)
		}
		return nil
	}
}

// visualExitHandler returns the Handler closure for <esc> in Visual
// modes. Clears Selection via editor.ExitVisual and flips QUERY_EDITOR
// mode back to ModeNormal.
func (c *VimEditorController) visualExitHandler() commands.Handler {
	return func(_ commands.ExecCtx) error {
		buf := c.buffer()
		if buf == nil {
			return nil
		}
		editor.ExitVisual(buf)
		if c.qec != nil {
			c.qec.SetMode(types.ModeNormal)
		}
		return nil
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
// Dispatch:
//   - OperatorPending → resolve range, hand off to applyPending (stub
//     in wwd.5; wwd.8 fills the body).
//   - Visual / VisualLine → resolve range, snap Buffer.Selection to it
//     via editor.SetSelection. Per Architecture Decision 4 of the wwd
//     epic, Visual + textobject bypasses op-pending entirely.
//   - else → no-op.
func (c *VimEditorController) textObjectHandler(spec textObjectSpec) commands.Handler {
	return func(ec commands.ExecCtx) error {
		buf := c.buffer()
		if buf == nil {
			return nil
		}
		from := buf.CursorPos()
		r, ok := spec.fn(buf, from)
		if !ok {
			return nil
		}
		if ec.Mode.Has(types.ModeVisual | types.ModeVisualLine) {
			editor.SetSelection(buf, &r)
			return nil
		}
		if ec.Mode.Has(types.ModeOperatorPending) {
			return c.applyPending(buf, r)
		}
		return nil
	}
}

// motionHandler returns the Handler closure for one motionSpec.
//
// Dispatch order (mode-driven; operator-pending wins over visual since
// the two cannot coexist in practice):
//   - OperatorPending → applyPending(Range{from, newPos})
//   - Visual / VisualLine / VisualBlock → ExtendSelection(newPos); jumps
//     are NOT pushed (vim doesn't push during visual extension).
//   - else → SetCursor(newPos) + push jump when motion is classed jump.
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
		if ec.Mode.Has(types.ModeVisual | types.ModeVisualLine | types.ModeVisualBlock) {
			editor.ExtendSelection(buf, newPos)
			return nil
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
