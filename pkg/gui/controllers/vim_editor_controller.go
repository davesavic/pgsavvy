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

	// toaster is the optional sink for one-shot user feedback (e.g. the
	// `"+y` / `"*y` "system clipboard not wired" TODO toast). Wired
	// post-construction via SetToaster so test wiring can keep the
	// two-arg NewVimEditorController call shape.
	toaster func(msg string)

	// clipboardToasted records whether the +/* TODO toast already fired
	// this session — vim's system-clipboard registers (+/*) fall back to
	// the in-memory store and surface a one-shot notice only.
	clipboardToasted bool
}

// NewVimEditorController constructs the controller. Either argument
// may be nil — the controller silently no-ops when its buffer or
// matcher is missing. The orchestrator wires concrete values; tests
// may pass nil to exercise the GetKeybindings / RegisterActions
// surface independently.
func NewVimEditorController(qec *context.QueryEditorContext, matcher *keys.Matcher) *VimEditorController {
	return &VimEditorController{qec: qec, matcher: matcher}
}

// SetToaster wires a toast sink for the controller. nil clears any
// previously-installed sink. The orchestrator calls this in
// AttachControllers (post-construction) with a closure over the live
// ToastHelper. Unit tests typically leave the sink unset.
func (c *VimEditorController) SetToaster(t func(msg string)) {
	c.toaster = t
}

// emitToast forwards msg to the wired toaster, or drops it when no
// sink is installed.
func (c *VimEditorController) emitToast(msg string) {
	if c.toaster == nil {
		return
	}
	c.toaster(msg)
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

// operatorModeMask is the Mode mask under which operator bindings fire.
// Operators are valid in Normal (initiates op-pending), every Visual
// variant (consumes Selection directly per Architecture Decision 4),
// AND OperatorPending (the second key triggers the linewise variant —
// `dd`/`yy`/`cc`/`>>`/`<<` etc.).
const operatorModeMask = types.ModeNormal | types.ModeOperatorPending |
	types.ModeVisual | types.ModeVisualLine | types.ModeVisualBlock

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

// insertEntryModeMask is the Mode mask under which the insert-entry
// bindings (i / a / o / O / I / A) fire — Normal only. Re-pressing
// `i` while already in Insert mode would be the literal rune; the
// Normal-only mask keeps that path clean.
const insertEntryModeMask = types.ModeNormal

// insertExitModeMask is the Mode mask under which `<esc>` fires the
// mode.normal action. wwd.10 shipped this as Insert-only; wwd.8 extends
// it to also cover OperatorPending so a half-typed operator can be
// cancelled with `<esc>` (clears RepeatStore.PendingOpID + resets mode).
// Visual `<esc>` is bound separately to visual.exit and does not
// overlap (the modes are disjoint bits).
const insertExitModeMask = types.ModeInsert | types.ModeOperatorPending

// editorHistoryModeMask is the Mode mask under which u / <c-r> fire —
// Normal only. Vim's undo/redo are Normal-mode commands.
const editorHistoryModeMask = types.ModeNormal

// pasteModeMask is the Mode mask under which `p` (paste) fires — Normal
// only in wwd.8. Visual-mode paste (replacing selection with register
// contents) is deferred.
const pasteModeMask = types.ModeNormal

// editorRepeatModeMask is the Mode mask under which `.` fires — Normal
// only. Replaying a captured operator from Visual or OperatorPending
// would conflict with the live state machine, so wwd.9 restricts replay
// to the Normal-mode entry point.
const editorRepeatModeMask = types.ModeNormal

// operatorSpec ties a shorthand to an operator action ID + its apply
// function. apply receives a Range and returns the captured register
// text (empty for non-yanking operators like gU/gu/>/<). isChange flips
// the mode to ModeInsert post-application (the vim `c` family).
type operatorSpec struct {
	shorthand   string
	chord       []keys.Key // overrides shorthand parsing when non-nil
	actionID    string
	description string
	apply       func(b *editor.Buffer, r editor.Range) (capture string, err error)
	isChange    bool
}

// operatorSpecs returns the wwd.8 operator binding table.
//
//   - d/y/c (delete/yank/change): char-wise apply.
//   - gU/gu (upper/lower): char-wise replace, no register write.
//   - >/<  (indent right/left): linewise indent by ShiftWidth (=2).
//
// `<` and `>` cannot be shorthand-parsed (the parser treats `<` as the
// start of a `<...>` token); their chords are constructed manually as
// single-rune Keys.
func (c *VimEditorController) operatorSpecs() []operatorSpec {
	return []operatorSpec{
		{
			shorthand:   "d",
			actionID:    commands.OperatorDelete,
			description: "delete",
			apply: func(b *editor.Buffer, r editor.Range) (string, error) {
				return editor.Delete(b, r)
			},
		},
		{
			shorthand:   "y",
			actionID:    commands.OperatorYank,
			description: "yank",
			apply: func(b *editor.Buffer, r editor.Range) (string, error) {
				return editor.Yank(b, r), nil
			},
		},
		{
			shorthand:   "c",
			actionID:    commands.OperatorChange,
			description: "change",
			apply: func(b *editor.Buffer, r editor.Range) (string, error) {
				return editor.Change(b, r)
			},
			isChange: true,
		},
		{
			shorthand:   "gU",
			actionID:    commands.OperatorUpper,
			description: "uppercase",
			apply: func(b *editor.Buffer, r editor.Range) (string, error) {
				return "", editor.Upper(b, r)
			},
		},
		{
			shorthand:   "gu",
			actionID:    commands.OperatorLower,
			description: "lowercase",
			apply: func(b *editor.Buffer, r editor.Range) (string, error) {
				return "", editor.Lower(b, r)
			},
		},
		{
			chord:       []keys.Key{{Code: '>'}},
			actionID:    commands.OperatorIndentRight,
			description: "indent right",
			apply: func(b *editor.Buffer, r editor.Range) (string, error) {
				return "", editor.IndentRight(b, r.Start.Line, r.End.Line)
			},
		},
		{
			chord:       []keys.Key{{Code: '<'}},
			actionID:    commands.OperatorIndentLeft,
			description: "indent left",
			apply: func(b *editor.Buffer, r editor.Range) (string, error) {
				return "", editor.IndentLeft(b, r.Start.Line, r.End.Line)
			},
		},
	}
}

// findOperatorSpec returns the operatorSpec whose actionID matches id,
// or (operatorSpec{}, false) when no operator owns the ID. Used by
// applyPending to dispatch the stashed PendingOpID.
func (c *VimEditorController) findOperatorSpec(id string) (operatorSpec, bool) {
	for _, s := range c.operatorSpecs() {
		if s.actionID == id {
			return s, true
		}
	}
	return operatorSpec{}, false
}

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
	inserts := c.insertEntrySpecs()
	histories := c.editorHistorySpecs()
	operators := c.operatorSpecs()
	out := make([]*types.ChordBinding, 0, len(specs)+len(textObjects)+len(visuals)+len(inserts)+len(histories)+len(operators)+1)
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
	for _, s := range inserts {
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
			Tag:         s.tag,
		})
	}
	for _, s := range histories {
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
			Tag:         s.tag,
		})
	}
	// Paste binding: `p` Normal-only.
	if seq, err := keys.SequenceFromShorthand("p"); err == nil {
		out = append(out, &types.ChordBinding{
			Sequence:    seq,
			Mode:        pasteModeMask,
			Scope:       types.QUERY_EDITOR,
			ActionID:    commands.EditorPaste,
			Description: "paste after cursor",
			Tag:         "Edit",
		})
	}
	// Repeat binding: `.` Normal-only (wwd.9).
	if seq, err := keys.SequenceFromShorthand("."); err == nil {
		out = append(out, &types.ChordBinding{
			Sequence:    seq,
			Mode:        editorRepeatModeMask,
			Scope:       types.QUERY_EDITOR,
			ActionID:    commands.EditorRepeat,
			Description: "repeat last edit",
			Tag:         "Edit history",
		})
	}
	for _, s := range operators {
		var (
			seq []keys.Key
			err error
		)
		if s.chord != nil {
			seq = append([]keys.Key(nil), s.chord...)
		} else {
			seq, err = keys.SequenceFromShorthand(s.shorthand)
			if err != nil {
				continue
			}
		}
		out = append(out, &types.ChordBinding{
			Sequence:    seq,
			Mode:        operatorModeMask,
			Scope:       types.QUERY_EDITOR,
			ActionID:    s.actionID,
			Description: s.description,
			Tag:         "Operator",
		})
	}
	// (op-pending cancel: the existing `<esc>` → mode.normal binding from
	// the insert-entry specs covers OperatorPending too — its mode mask
	// was widened in wwd.8 via insertExitModeMask.)
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

// editorActionSpec ties a shorthand to an action ID and its mode mask.
// Used for the insert-entry, mode.normal, and undo/redo bindings added
// by wwd.10. Each entry resolves to a dedicated handler in
// RegisterActions; no shared closure family like motionHandler.
type editorActionSpec struct {
	shorthand   string
	actionID    string
	description string
	tag         string
	mode        types.Mode
}

// insertEntrySpecs returns the wwd.10 insert-entry + mode.normal table.
// Insert entries fire only from Normal; the `<esc>` mode.normal exit
// fires only from Insert. Tag drives the cheatsheet section heading.
func (c *VimEditorController) insertEntrySpecs() []editorActionSpec {
	return []editorActionSpec{
		{"i", commands.InsertEnter, "enter insert", "Insert", insertEntryModeMask},
		{"a", commands.InsertAppend, "append after cursor", "Insert", insertEntryModeMask},
		{"o", commands.InsertOpenBelow, "open line below", "Insert", insertEntryModeMask},
		{"O", commands.InsertOpenAbove, "open line above", "Insert", insertEntryModeMask},
		{"I", commands.InsertFirstNonblank, "insert at first non-blank", "Insert", insertEntryModeMask},
		{"A", commands.InsertAppendEnd, "append at line end", "Insert", insertEntryModeMask},
		{"<esc>", commands.ModeNormal, "exit insert", "Insert", insertExitModeMask},
	}
}

// editorHistorySpecs returns the wwd.10 undo / redo bindings.
func (c *VimEditorController) editorHistorySpecs() []editorActionSpec {
	return []editorActionSpec{
		{"u", commands.EditorUndo, "undo", "Edit history", editorHistoryModeMask},
		{"<c-r>", commands.EditorRedo, "redo", "Edit history", editorHistoryModeMask},
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
	// wwd.10 — insert-entry + mode.normal + undo/redo handlers.
	_ = reg.Register(&commands.Command{
		ID:          commands.InsertEnter,
		Description: "Enter insert mode at cursor",
		Tag:         "Insert",
		Handler:     c.insertEnterHandler(),
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.InsertAppend,
		Description: "Enter insert mode after cursor",
		Tag:         "Insert",
		Handler:     c.insertAppendHandler(),
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.InsertOpenBelow,
		Description: "Open new line below and enter insert",
		Tag:         "Insert",
		Handler:     c.insertOpenBelowHandler(),
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.InsertOpenAbove,
		Description: "Open new line above and enter insert",
		Tag:         "Insert",
		Handler:     c.insertOpenAboveHandler(),
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.InsertFirstNonblank,
		Description: "Enter insert at first non-blank column",
		Tag:         "Insert",
		Handler:     c.insertFirstNonblankHandler(),
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.InsertAppendEnd,
		Description: "Enter insert at line end",
		Tag:         "Insert",
		Handler:     c.insertAppendEndHandler(),
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ModeNormal,
		Description: "Exit insert mode",
		Tag:         "Insert",
		Handler:     c.modeNormalHandler(),
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.EditorUndo,
		Description: "Undo last edit",
		Tag:         "Edit history",
		Handler:     c.undoHandler(),
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.EditorRedo,
		Description: "Redo last undone edit",
		Tag:         "Edit history",
		Handler:     c.redoHandler(),
	})
	// wwd.8 — operator handlers. Each spec gets its own Handler closure
	// over the spec value (loop-var aliasing avoided).
	for _, s := range c.operatorSpecs() {
		spec := s
		_ = reg.Register(&commands.Command{
			ID:          spec.actionID,
			Description: spec.description,
			Tag:         "Operator",
			Handler:     c.operatorHandler(spec),
		})
	}
	_ = reg.Register(&commands.Command{
		ID:          commands.EditorPaste,
		Description: "Paste after cursor",
		Tag:         "Edit",
		Handler:     c.pasteHandler(),
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.EditorRepeat,
		Description: "Repeat last edit (vim `.`)",
		Tag:         "Edit history",
		Handler:     c.repeatHandler(),
	})
}

// findMotionSpec returns the motionSpec whose actionID matches id, or
// (motionSpec{}, false) when no motion owns the ID. Used by the `.`
// handler to look up the original motion function and re-resolve its
// range from the CURRENT cursor (vim semantics).
func (c *VimEditorController) findMotionSpec(id string) (motionSpec, bool) {
	if id == "" {
		return motionSpec{}, false
	}
	for _, s := range c.motionSpecs() {
		if s.actionID == id {
			return s, true
		}
	}
	return motionSpec{}, false
}

// findTextObjectSpec returns the textObjectSpec whose actionID matches
// id, or (textObjectSpec{}, false). iB / aB and i{ / a{ alias to the
// same actionID; the first match is sufficient for replay.
func (c *VimEditorController) findTextObjectSpec(id string) (textObjectSpec, bool) {
	if id == "" {
		return textObjectSpec{}, false
	}
	for _, s := range c.textObjectSpecs() {
		if s.actionID == id {
			return s, true
		}
	}
	return textObjectSpec{}, false
}

// repeatHandler returns the `.` action handler. Re-resolves the captured
// motion or text-object range from the CURRENT cursor and re-invokes the
// stashed operator via the same applyPending pathway used during the
// original dispatch. Empty RepeatStore is a silent no-op.
//
// Re-resolution semantics: `.` is NOT a snapshotted replay — vim re-runs
// the motion/text-object against the current cursor so a sequence like
// `dap` (delete a paragraph), j (next line), `.` (delete a paragraph)
// deletes the paragraph the cursor is in NOW, not the one originally
// targeted.
//
// Operator dispatch reuses applyPending so the `c` (change) mode flip
// to Insert and register/clipboard wiring stay consistent between the
// first dispatch and the replay.
func (c *VimEditorController) repeatHandler() commands.Handler {
	return func(ec commands.ExecCtx) error {
		buf := c.buffer()
		if buf == nil || c.qec == nil {
			return nil
		}
		rep := c.qec.Repeat()
		if rep == nil {
			return nil
		}
		opID, count, reg, ok := rep.Replay()
		if !ok {
			return nil
		}
		// Re-resolve the range from the CURRENT cursor.
		from := buf.CursorPos()
		var (
			r            editor.Range
			ranged       bool
			rebuiltMotID string
			rebuiltTxtID string
		)
		switch {
		case rep.LastMotionID != "":
			ms, ok := c.findMotionSpec(rep.LastMotionID)
			if !ok {
				return nil
			}
			rebuiltMotID = ms.actionID
			replayCount := count
			if replayCount == 0 {
				replayCount = 1
			}
			newPos, motionOK := ms.fn(buf, from, replayCount)
			if !motionOK {
				// At boundary — operate over zero-length range so the
				// applyPending state machine still resets cleanly.
				r = editor.Range{Start: from, End: from}
			} else {
				r = editor.Range{Start: from, End: newPos}
			}
			ranged = true
		case rep.LastTextObjectID != "":
			tos, ok := c.findTextObjectSpec(rep.LastTextObjectID)
			if !ok {
				return nil
			}
			rebuiltTxtID = tos.actionID
			rng, resolved := tos.fn(buf, from)
			if !resolved {
				return nil
			}
			r = rng
			ranged = true
		}
		// Stash PendingOpID so applyPending dispatches via the normal
		// operator pathway. Synthesize an ExecCtx with the replayed
		// count + register so register-aware operators (y/d/c) write
		// to the same effective register the original dispatch used.
		rep.PendingOpID = opID
		replayCtx := commands.ExecCtx{
			Count:    count,
			Register: reg,
			Mode:     types.ModeOperatorPending,
			Scope:    ec.Scope,
		}
		if !ranged {
			// Doubled-shortcut replay (dd/yy/cc/>>/<<). Run the same
			// linewise dispatch path the original handler used.
			spec, specOK := c.findOperatorSpec(opID)
			if !specOK {
				rep.PendingOpID = ""
				return nil
			}
			return c.operatorDoubledApply(buf, spec, replayCtx)
		}
		return c.applyPending(buf, r, rebuiltMotID, rebuiltTxtID, replayCtx)
	}
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
			return c.applyPending(buf, r, "", spec.actionID, ec)
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
			// Op-pending boundary: a motion at its target (G from last
			// line, gg from {0,0}, 0 at col 0) returns ok=false. Vim's
			// `dG` from the last line still deletes that line — a
			// zero-length range is meaningful for operators. Hand the
			// zero-length Range{from,from} to applyPending so the
			// operator can act (typically a no-op, but the state
			// machine still resets cleanly).
			if ec.Mode.Has(types.ModeOperatorPending) {
				return c.applyPending(buf, editor.Range{Start: from, End: from}, spec.actionID, "", ec)
			}
			return nil
		}
		if ec.Mode.Has(types.ModeOperatorPending) {
			return c.applyPending(buf, editor.Range{Start: from, End: newPos}, spec.actionID, "", ec)
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

// applyPending completes an operator-pending dispatch. The motion or
// text-object that fired in OperatorPending mode supplies r as the
// resolved range; applyPending reads c.qec.Repeat().PendingOpID,
// looks up the operator spec, invokes its apply function with the
// register-aware ExecCtx routing, then resets mode + pending stash.
//
//	completedMotionID     — populated when motionHandler called us
//	completedTextObjectID — populated when textObjectHandler called us
//
// The two are mutually exclusive (a motion XOR a text-object completes
// the operator). Both empty is acceptable for the doubled-shortcut path
// (dd/yy/cc/>>/<<) where the operator handler self-completes against
// the current line.
//
// After applying, applyPending updates RepeatStore.{LastOpID, LastMotionID,
// LastTextObjectID, LastCount, LastRegister} so wwd.9's `.` action can
// replay. PendingOpID is cleared on every exit path.
func (c *VimEditorController) applyPending(buf *editor.Buffer, r editor.Range, completedMotionID, completedTextObjectID string, ctx commands.ExecCtx) error {
	if c.qec == nil || buf == nil {
		return nil
	}
	rep := c.qec.Repeat()
	if rep == nil {
		return nil
	}
	pendingOpID := rep.PendingOpID
	// Clear pending eagerly so any early-return path leaves the
	// state machine clean.
	defer func() {
		rep.PendingOpID = ""
	}()
	if pendingOpID == "" {
		// No operator pending — defensive guard. Reset to Normal in
		// case caller left ModeOperatorPending set.
		c.setMode(types.ModeNormal)
		return nil
	}
	spec, ok := c.findOperatorSpec(pendingOpID)
	if !ok {
		c.setMode(types.ModeNormal)
		return nil
	}
	r = editor.NormaliseRange(r)
	capture, err := spec.apply(buf, r)
	if err != nil {
		c.setMode(types.ModeNormal)
		return err
	}
	if capture != "" {
		c.writeRegister(ctx.Register, capture)
	}
	// Repeat-store bookkeeping (wwd.9 will consume this for `.`).
	rep.LastOpID = pendingOpID
	rep.LastMotionID = completedMotionID
	rep.LastTextObjectID = completedTextObjectID
	rep.LastCount = ctx.Count
	rep.LastRegister = ctx.Register
	// Mode transition: `change` lands in Insert; everything else
	// returns to Normal.
	if spec.isChange {
		c.setMode(types.ModeInsert)
	} else {
		c.setMode(types.ModeNormal)
	}
	return nil
}

// operatorHandler returns the Handler closure for one operatorSpec.
//
// Dispatch:
//   - Visual / VisualLine / VisualBlock — consume Buffer.Selection
//     directly, apply, write register, exit Visual, return. (Architecture
//     Decision 4: Visual+operator bypasses op-pending.)
//   - OperatorPending — same-key second press = doubled-shortcut variant
//     (dd/yy/cc/>>/<<): operate linewise on the current line. Different
//     operator key in op-pending overrides the stash and acts as a fresh
//     doubled-shortcut on the current line.
//   - Normal — stash PendingOpID, set ModeOperatorPending, wait for the
//     completing motion / text-object.
func (c *VimEditorController) operatorHandler(spec operatorSpec) commands.Handler {
	return func(ec commands.ExecCtx) error {
		buf := c.buffer()
		if buf == nil {
			return nil
		}
		// System-clipboard registers (+/*): emit one-shot TODO toast on
		// first use this session, then fall through to the in-memory
		// store. Subsequent uses are silent.
		if ec.Register == '+' || ec.Register == '*' {
			if !c.clipboardToasted {
				c.emitToast("register + / * not yet wired to system clipboard")
				c.clipboardToasted = true
			}
		}
		// Visual-mode bypass — consume Selection.
		if ec.Mode.Has(types.ModeVisual | types.ModeVisualLine | types.ModeVisualBlock) {
			return c.operatorVisualApply(buf, spec, ec)
		}
		// OperatorPending — doubled-shortcut path (dd/yy/cc/>>/<<).
		if ec.Mode.Has(types.ModeOperatorPending) {
			return c.operatorDoubledApply(buf, spec, ec)
		}
		// Normal — stash and enter OperatorPending.
		if c.qec == nil {
			return nil
		}
		rep := c.qec.Repeat()
		if rep == nil {
			return nil
		}
		rep.PendingOpID = spec.actionID
		c.setMode(types.ModeOperatorPending)
		return nil
	}
}

// operatorVisualApply runs spec.apply over the live Buffer.Selection,
// writes the captured text to the effective register, exits Visual
// (clears Selection and flips mode back to Normal — or Insert for
// `change`), and records the operation in RepeatStore for `.` replay.
func (c *VimEditorController) operatorVisualApply(buf *editor.Buffer, spec operatorSpec, ec commands.ExecCtx) error {
	if buf.Selection == nil {
		// Visual mode without an active Selection is defensive — exit
		// cleanly so we don't strand state.
		editor.ExitVisual(buf)
		c.setMode(types.ModeNormal)
		return nil
	}
	sel := *buf.Selection
	if ec.Mode.Has(types.ModeVisualLine) {
		sel = editor.LineWiseFromVisualLine(buf, sel)
	} else {
		sel = editor.NormaliseRange(sel)
	}
	capture, err := spec.apply(buf, sel)
	if err != nil {
		editor.ExitVisual(buf)
		c.setMode(types.ModeNormal)
		return err
	}
	if capture != "" {
		c.writeRegister(ec.Register, capture)
	}
	editor.ExitVisual(buf)
	if rep := c.repeat(); rep != nil {
		rep.LastOpID = spec.actionID
		rep.LastMotionID = ""
		rep.LastTextObjectID = ""
		rep.LastCount = ec.Count
		rep.LastRegister = ec.Register
		rep.PendingOpID = ""
	}
	if spec.isChange {
		c.setMode(types.ModeInsert)
	} else {
		c.setMode(types.ModeNormal)
	}
	return nil
}

// operatorDoubledApply implements the dd/yy/cc/>>/<< linewise variant.
// Operates over the current line (or count lines starting at cursor when
// a count prefix was supplied), then records the op into RepeatStore.
func (c *VimEditorController) operatorDoubledApply(buf *editor.Buffer, spec operatorSpec, ec commands.ExecCtx) error {
	count := ec.Count
	if count == 0 {
		count = 1
	}
	cursor := buf.CursorPos()
	r := editor.CurrentLineLineWise(buf, cursor, count)
	capture, err := spec.apply(buf, r)
	if err != nil {
		c.setMode(types.ModeNormal)
		return err
	}
	if capture != "" {
		c.writeRegister(ec.Register, capture)
	}
	if rep := c.repeat(); rep != nil {
		rep.LastOpID = spec.actionID
		rep.LastMotionID = ""
		rep.LastTextObjectID = ""
		rep.LastCount = count
		rep.LastRegister = ec.Register
		rep.PendingOpID = ""
	}
	if spec.isChange {
		c.setMode(types.ModeInsert)
	} else {
		c.setMode(types.ModeNormal)
	}
	return nil
}

// writeRegister stores text in the effective register. ec.Register == 0
// → effective register is `"` (rune 0x22) per the vim default-register
// contract documented on commands.ExecCtx. A nil matcher (test wiring)
// makes the call a no-op.
func (c *VimEditorController) writeRegister(reg rune, text string) {
	if c.matcher == nil {
		return
	}
	store := c.matcher.Registers()
	if store == nil {
		return
	}
	if reg == 0 {
		reg = '"'
	}
	store.Set(reg, text)
}

// repeat is the nil-safe RepeatStore accessor. Returns nil when qec is
// unwired (test fixtures) so callers fall through without panicking.
func (c *VimEditorController) repeat() *editor.RepeatStore {
	if c.qec == nil {
		return nil
	}
	return c.qec.Repeat()
}

// readRegister returns the contents of the effective register (defaulting
// to '"' when reg == 0). Empty string when no matcher / no register
// store / register unset.
func (c *VimEditorController) readRegister(reg rune) string {
	if c.matcher == nil {
		return ""
	}
	store := c.matcher.Registers()
	if store == nil {
		return ""
	}
	if reg == 0 {
		reg = '"'
	}
	return store.Get(reg)
}

// pasteHandler returns the `p` paste handler. Inserts the contents of
// the effective register at / after the cursor:
//
//   - Char-wise register: inserted after the cursor column (or at end
//     of an empty buffer / column-0 of an empty line).
//   - Line-wise register (text ends in '\n', set by dd/yy doubled-form):
//     inserted on a new line below the cursor.
//
// Empty register is a no-op. System-clipboard registers (+/*) surface
// the same one-shot TODO toast as the yank operator before falling
// through to the in-memory store.
func (c *VimEditorController) pasteHandler() commands.Handler {
	return func(ec commands.ExecCtx) error {
		buf := c.buffer()
		if buf == nil {
			return nil
		}
		if ec.Register == '+' || ec.Register == '*' {
			if !c.clipboardToasted {
				c.emitToast("register + / * not yet wired to system clipboard")
				c.clipboardToasted = true
			}
		}
		text := c.readRegister(ec.Register)
		if text == "" {
			return nil
		}
		cur := buf.CursorPos()
		// Line-wise: trailing newline marker (vim's '\n'-terminated
		// register contents from a dd/yy yank). Strip it and insert on
		// the next line.
		if len(text) > 0 && text[len(text)-1] == '\n' {
			body := text[:len(text)-1]
			// Insert at end of current line + "\n" + body, so the
			// pasted lines land below the cursor.
			endCol := buf.LineRuneLen(cur.Line)
			pos := editor.Position{Line: cur.Line, Col: endCol}
			if err := buf.Apply(editor.Edit{
				Kind:  editor.EditKindInsert,
				Range: editor.Range{Start: pos, End: pos},
				Text:  "\n" + body,
			}); err != nil {
				return err
			}
			// Cursor moves to col 0 of the first pasted line.
			buf.SetCursor(editor.Position{Line: cur.Line + 1, Col: 0})
			return nil
		}
		// Char-wise: insert after cursor (vim's `p` semantics). At
		// the end-of-line column, "after cursor" lands at the end.
		pasteCol := cur.Col + 1
		maxCol := buf.LineRuneLen(cur.Line)
		if pasteCol > maxCol {
			pasteCol = maxCol
		}
		pos := editor.Position{Line: cur.Line, Col: pasteCol}
		if err := buf.Apply(editor.Edit{
			Kind:  editor.EditKindInsert,
			Range: editor.Range{Start: pos, End: pos},
			Text:  text,
		}); err != nil {
			return err
		}
		return nil
	}
}

// buffer returns the live *editor.Buffer of the wired
// QueryEditorContext, or nil when qec is nil (test wiring).
func (c *VimEditorController) buffer() *editor.Buffer {
	if c.qec == nil {
		return nil
	}
	return c.qec.Buffer()
}

// setMode flips QUERY_EDITOR mode to m via the wired QueryEditorContext.
// No-op when qec is nil (test wiring without modes setter).
func (c *VimEditorController) setMode(m types.Mode) {
	if c.qec == nil {
		return
	}
	c.qec.SetMode(m)
}

// insertEnterHandler returns the `i` handler: leave Cursor in place,
// flip to ModeInsert.
func (c *VimEditorController) insertEnterHandler() commands.Handler {
	return func(_ commands.ExecCtx) error {
		if c.buffer() == nil {
			return nil
		}
		c.setMode(types.ModeInsert)
		return nil
	}
}

// insertAppendHandler returns the `a` handler: move Cursor one column
// right (clamped to line-end+1), then flip to ModeInsert.
func (c *VimEditorController) insertAppendHandler() commands.Handler {
	return func(_ commands.ExecCtx) error {
		buf := c.buffer()
		if buf == nil {
			return nil
		}
		cur := buf.CursorPos()
		next := editor.Position{Line: cur.Line, Col: cur.Col + 1}
		maxCol := buf.LineRuneLen(cur.Line)
		if next.Col > maxCol {
			next.Col = maxCol
		}
		buf.SetCursor(next)
		c.setMode(types.ModeInsert)
		return nil
	}
}

// insertOpenBelowHandler returns the `o` handler: insert "\n" at the
// end of the current line, move Cursor to start of the new line, flip
// to ModeInsert. On an empty buffer this lazily seeds Lines[0] before
// the edit so Buffer.Apply has a valid Position{0,0}.
func (c *VimEditorController) insertOpenBelowHandler() commands.Handler {
	return func(_ commands.ExecCtx) error {
		buf := c.buffer()
		if buf == nil {
			return nil
		}
		cur := buf.CursorPos()
		// Insert at the end of the current line so the new line lands
		// directly below.
		end := editor.Position{Line: cur.Line, Col: buf.LineRuneLen(cur.Line)}
		if err := buf.Apply(editor.Edit{
			Kind:  editor.EditKindInsert,
			Range: editor.Range{Start: end, End: end},
			Text:  "\n",
		}); err != nil {
			return nil
		}
		buf.SetCursor(editor.Position{Line: cur.Line + 1, Col: 0})
		c.setMode(types.ModeInsert)
		return nil
	}
}

// insertOpenAboveHandler returns the `O` handler: insert "\n" at the
// start of the current line, leave Cursor on the original line index
// (which is now the new blank line), flip to ModeInsert.
func (c *VimEditorController) insertOpenAboveHandler() commands.Handler {
	return func(_ commands.ExecCtx) error {
		buf := c.buffer()
		if buf == nil {
			return nil
		}
		cur := buf.CursorPos()
		start := editor.Position{Line: cur.Line, Col: 0}
		if err := buf.Apply(editor.Edit{
			Kind:  editor.EditKindInsert,
			Range: editor.Range{Start: start, End: start},
			Text:  "\n",
		}); err != nil {
			return nil
		}
		// The inserted "\n" splits the current line at column 0, leaving
		// an empty new line at cur.Line and pushing the original content
		// down. Cursor lands on the new empty line.
		buf.SetCursor(editor.Position{Line: cur.Line, Col: 0})
		c.setMode(types.ModeInsert)
		return nil
	}
}

// insertFirstNonblankHandler returns the `I` handler: jump Cursor to
// the first non-blank column of the current line (or column 0 on an
// all-blank line / when LineFirstNonBlank reports ok=false because
// Cursor is already there), then flip to ModeInsert.
func (c *VimEditorController) insertFirstNonblankHandler() commands.Handler {
	return func(_ commands.ExecCtx) error {
		buf := c.buffer()
		if buf == nil {
			return nil
		}
		from := buf.CursorPos()
		if next, ok := editor.LineFirstNonBlank(buf, from, 1); ok {
			buf.SetCursor(next)
		}
		c.setMode(types.ModeInsert)
		return nil
	}
}

// insertAppendEndHandler returns the `A` handler: jump Cursor to
// line-end+1 (the append slot), then flip to ModeInsert.
func (c *VimEditorController) insertAppendEndHandler() commands.Handler {
	return func(_ commands.ExecCtx) error {
		buf := c.buffer()
		if buf == nil {
			return nil
		}
		from := buf.CursorPos()
		end := editor.Position{Line: from.Line, Col: buf.LineRuneLen(from.Line)}
		buf.SetCursor(end)
		c.setMode(types.ModeInsert)
		return nil
	}
}

// modeNormalHandler returns the `<esc>` handler used in both Insert
// and OperatorPending modes. Flips QUERY_EDITOR mode back to ModeNormal
// and (for OperatorPending) clears RepeatStore.PendingOpID so a
// half-typed operator doesn't strand later dispatches.
func (c *VimEditorController) modeNormalHandler() commands.Handler {
	return func(_ commands.ExecCtx) error {
		if c.buffer() == nil {
			return nil
		}
		if rep := c.repeat(); rep != nil {
			rep.PendingOpID = ""
		}
		c.setMode(types.ModeNormal)
		return nil
	}
}

// undoHandler returns the `u` handler. Delegates to Buffer.Undo, which
// rewinds the History cursor and replays the inverse Edit. Empty
// history is a silent no-op. cancelSelectionIfOverlap is invoked
// internally by Buffer.Undo so a stale visual range is dropped.
func (c *VimEditorController) undoHandler() commands.Handler {
	return func(_ commands.ExecCtx) error {
		buf := c.buffer()
		if buf == nil {
			return nil
		}
		return buf.Undo()
	}
}

// redoHandler returns the `<c-r>` handler. Walks History forward along
// children[0] and re-applies the recorded forward Edit. Empty
// children list is a silent no-op.
func (c *VimEditorController) redoHandler() commands.Handler {
	return func(_ commands.ExecCtx) error {
		buf := c.buffer()
		if buf == nil {
			return nil
		}
		return buf.Redo()
	}
}
