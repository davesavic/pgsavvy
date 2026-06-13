package controllers

import (
	stdctx "context"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/davesavic/dbsavvy/pkg/gui/clipboard"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/editor"
	"github.com/davesavic/dbsavvy/pkg/gui/editor/sqlcontext"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
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

	// clipboard is the optional OS-clipboard seam. Nil => internal-only
	// (named registers + in-memory store), no behavior change. Wired
	// post-construction via SetClipboard so test wiring can keep the
	// two-arg NewVimEditorController call shape.
	clipboard clipboard.Clipboard

	// lastWrite is the last text this controller mirrored to the OS
	// clipboard (unnormalized). readRegister uses it to detect external
	// clipboard changes: when the clipboard differs from lastWrite the
	// external content wins (unnamedplus semantics).
	lastWrite string

	// flasher is the optional post-yank highlight seam (Neovim on_yank
	// parity). Nil => no flash (no behavior change). Wired post-construction
	// via SetYankFlasher so test wiring can keep the two-arg
	// NewVimEditorController call shape. Fired only on the yank operator.
	flasher YankFlasher

	// completionEngine is the optional completion driver. Wired
	// post-construction via SetCompletionEngine. Nil = TriggerCompletion
	// is a silent no-op.
	completionEngine *editor.Engine

	// suggestions is the optional SuggestionsContext the controller
	// pushes completion results into. Wired post-construction via
	// SetSuggestionsContext. Nil = TriggerCompletion is a silent no-op.
	suggestions *context.SuggestionsContext

	// suppressNextAutoTrigger guards against re-popup-after-accept: an
	// accept inserts a full identifier whose tail still satisfies the
	// AutoTriggerFromContext gate (e.g. `FROM users`), so the very next
	// as-you-type AutoTrigger would spuriously re-open the popup over the
	// just-accepted text. acceptSuggestion sets this one-shot flag;
	// AutoTrigger consumes and clears it, then requires a fresh keystroke.
	// dbsavvy-etp.4.
	suppressNextAutoTrigger bool

	// lastTriggerPos records the cursor position the controller last ran the
	// completion engine at (RefilterOrTrigger). The async warm re-trigger
	// bridge (OnWarmLanded) compares it against the live cursor to DROP a late
	// warm whose result no longer matches where the user is — the stale-trigger
	// guard. dbsavvy-ko4m.2.3.
	lastTriggerPos editor.Position

	// aliasOnAccept gates the auto-insert of an editable, deduped table
	// alias when a table candidate is accepted in a table context (e.g.
	// accepting `users` after `FROM ` inserts `users u`). DEFAULT ON
	// (set true in NewVimEditorController); the orchestrator flips it off
	// per `editor.autocomplete_alias: false`. When off, table accept inserts
	// the bare candidate text. dbsavvy-ko4m.6.2 (Finding K).
	aliasOnAccept bool

	// schemaMeta is the synchronous, race-safe metadata reader used at
	// accept time to count, across the in-scope tables, how many own the
	// accepted column — the ambiguity signal that drives auto-qualification
	// (dbsavvy-ko4m.6.3). Same warmed snapshot store the completion sources
	// read; the (cols, ok) ok-return is the warmed-or-not signal. Wired
	// post-construction via SetSchemaMetadata. Nil => no qualification (bare
	// column accept).
	schemaMeta editor.SchemaMetadata

	// schemaProv resolves the active schema name for an in-scope table that
	// carries no explicit schema qualifier (mirrors SchemaSource.schemaFor:
	// TableRef.Schema when set, else this provider). Wired alongside
	// schemaMeta via SetSchemaMetadata. Nil => active schema "".
	// dbsavvy-ko4m.6.3.
	schemaProv editor.SchemaProvider
}

// NewVimEditorController constructs the controller. Either argument
// may be nil — the controller silently no-ops when its buffer or
// matcher is missing. The orchestrator wires concrete values; tests
// may pass nil to exercise the GetKeybindings / RegisterActions
// surface independently.
func NewVimEditorController(qec *context.QueryEditorContext, matcher *keys.Matcher) *VimEditorController {
	return &VimEditorController{qec: qec, matcher: matcher, aliasOnAccept: true}
}

// SetAliasOnAccept toggles the auto-insert of an editable table alias on
// table-context accept (dbsavvy-ko4m.6.2, Finding K). Default ON; the
// orchestrator calls this with false when `editor.autocomplete_alias` is
// disabled. When off, accepting a table inserts the bare candidate text.
func (c *VimEditorController) SetAliasOnAccept(on bool) {
	c.aliasOnAccept = on
}

// SetSchemaMetadata wires the accept-time metadata reader + active-schema
// provider used to auto-qualify an ambiguous column on accept
// (dbsavvy-ko4m.6.3). meta is the same warmed snapshot store the completion
// sources read (it satisfies editor.SchemaMetadata); schema resolves the
// active schema for an unqualified in-scope table. A nil meta leaves the
// controller qualification-free (accept inserts the bare column). The
// orchestrator wires the live store + schema picker; tests inject a fake.
func (c *VimEditorController) SetSchemaMetadata(meta editor.SchemaMetadata, schema editor.SchemaProvider) {
	c.schemaMeta = meta
	c.schemaProv = schema
}

// SetClipboard wires the OS-clipboard seam the controller mirrors yanks
// to (and reads from) for the clipboard registers ('"' / '+' / '*').
// Nil leaves the controller internal-only (no behavior change). The
// orchestrator wires the live SystemClipboard; tests inject a fake.
func (c *VimEditorController) SetClipboard(cb clipboard.Clipboard) {
	c.clipboard = cb
}

// YankFlasher is the narrow post-yank highlight seam the controller fires
// after a yank. Declared in the controllers package (not helpers/ui) so the
// controllers package does not import helpers/ui — the concrete
// ui.YankFlashHelper is wired in by the orchestrator, which imports both.
type YankFlasher interface {
	Flash(buf *editor.Buffer, r editor.Range, ttl time.Duration)
}

// SetYankFlasher wires the post-yank highlight seam. Nil leaves the
// controller flash-free (no behavior change). The orchestrator wires the
// live ui.YankFlashHelper; tests inject a fake.
func (c *VimEditorController) SetYankFlasher(f YankFlasher) {
	c.flasher = f
}

// yankFlashTTL is how long the post-yank highlight stays armed before the
// scheduled clear fires (Neovim's default on_yank timeout).
const yankFlashTTL = 150 * time.Millisecond

// flashYank fires the post-yank highlight for r, gated to the yank operator
// only (delete/change also produce a non-empty capture but must not tint the
// just-removed text). No-op when no flasher is wired.
func (c *VimEditorController) flashYank(actionID string, buf *editor.Buffer, r editor.Range) {
	if c.flasher == nil || actionID != commands.OperatorYank {
		return
	}
	c.flasher.Flash(buf, r, yankFlashTTL)
}

// SetCompletionEngine wires the completion engine the controller
// invokes on the `<c-x><c-o>` insert-mode action. Nil disables the
// trigger (silent no-op). Z1 wires the live engine from setup; tests
// may pass their own engine.
func (c *VimEditorController) SetCompletionEngine(e *editor.Engine) {
	c.completionEngine = e
}

// CompletionEngineForTest exposes the wired completion engine so wiring
// tests can assert which sources were registered (dbsavvy-ko4m.7.3). Returns
// nil when no engine is wired.
func (c *VimEditorController) CompletionEngineForTest() *editor.Engine {
	return c.completionEngine
}

// SetSuggestionsContext wires the SuggestionsContext the controller
// pushes suggestions into on TriggerCompletion. Nil disables the
// popup surface (the engine still runs but its results have nowhere
// to land). Z1 wires the live context from setup.
func (c *VimEditorController) SetSuggestionsContext(s *context.SuggestionsContext) {
	c.suggestions = s
}

// TriggerCompletion is the `<c-x><c-o>` insert-mode action. Returns
// silently when the controller is not in Insert mode, when the
// engine is unwired, when the suggestions context is unwired, or
// when the engine returns no candidates. The keybinding registration
// for the action lives in Z1; this method is the action body Z1
// invokes from the central registry.
func (c *VimEditorController) TriggerCompletion() error {
	if c.qec == nil || c.completionEngine == nil || c.suggestions == nil {
		return nil
	}
	// Defensive mode check — Z1 registers the binding under an
	// Insert-only Mode mask so the dispatcher already gates this,
	// but a direct programmatic call (or a misconfigured binding)
	// must not pop the popup in Normal mode.
	if c.matcher != nil {
		if c.matcher.CurrentMode(types.QUERY_EDITOR) != types.ModeInsert {
			return nil
		}
	}
	buf := c.buffer()
	if buf == nil {
		return nil
	}
	c.RefilterOrTrigger(buf, buf.CursorPos())
	return nil
}

// RefilterOrTrigger re-runs the completion engine at pos and refreshes
// the popup in place: it Shows the candidates (popup visible, selection
// reset to the top match) or Hides when the engine returns nothing.
// This is the single place the refilter/trigger logic lives so the
// manual `<c-x><c-o>` action and dbsavvy-etp.4's as-you-type
// SetAutoCompleter callback share one code path. No-op when the engine
// or suggestions context is unwired. dbsavvy-etp.1.
func (c *VimEditorController) RefilterOrTrigger(buf *editor.Buffer, pos editor.Position) {
	if buf == nil || c.completionEngine == nil || c.suggestions == nil {
		return
	}
	// Record where we triggered so a late async warm landing can prove the
	// cursor has not moved before re-triggering (OnWarmLanded stale guard).
	c.lastTriggerPos = pos
	got := c.completionEngine.Trigger(stdctx.Background(), buf, pos)
	// Show with an empty set is a Hide (SuggestionsContext.Show contract),
	// so the empty-candidate dismiss is handled here without a branch.
	c.suggestions.Show(got, pos)
}

// OnWarmLanded is the re-trigger bridge the orchestrator wires into
// SchemaWarmer.SetOnWarmed. It fires on the UI loop after a (schema,table)
// warm completes, so the snapshot now holds the columns the in-flight Suggest
// returned empty for. It re-runs completion at the position the user is still
// at — but ONLY when the popup is still open AND the live cursor is exactly
// where the warm was requested (lastTriggerPos). A warm that lands after the
// user moved the cursor, dismissed the popup, or whose buffer is gone is
// DROPPED — the stale-trigger guard (dbsavvy-ko4m.2.3). The schema/table args
// are unused: the guard is purely positional, so a warm for any table the
// current context references refreshes the popup, and an unrelated warm is a
// harmless re-filter at the same position.
func (c *VimEditorController) OnWarmLanded(_ /*schema*/, _ /*table*/ string) {
	if c.suggestions == nil || !c.suggestions.IsVisible() {
		return
	}
	buf := c.buffer()
	if buf == nil {
		return
	}
	if buf.CursorPos() != c.lastTriggerPos {
		return
	}
	c.RefilterOrTrigger(buf, c.lastTriggerPos)
}

// AutoTrigger is the as-you-type callback installed via
// VimEditor.SetAutoCompleter at boot (only when editor.autocomplete is
// true — gui.go applies the config gate). Unlike the manual `<c-x><c-o>`
// path (RefilterOrTrigger, ungated), this is gated:
//
//   - popup already visible -> refilter in place at the new cursor
//     (so backspace-within-identifier and continued typing narrow it).
//   - popup hidden -> open ONLY when the cursor sits at an
//     AutoTriggerFromContext position (FROM/JOIN/UPDATE/INTO or
//     `<ident>.`); NOT prefix-everywhere.
//
// Manual `<c-x><c-o>` never routes through here, so the flag and the
// context gate never restrict it. No-op when the popup context is
// unwired. dbsavvy-etp.4.
func (c *VimEditorController) AutoTrigger(buf *editor.Buffer, pos editor.Position) {
	if c.suggestions == nil {
		return
	}
	if c.suppressNextAutoTrigger {
		c.suppressNextAutoTrigger = false
		// An explicit `<ident>.` column trigger overrides the post-accept
		// suppression: accepting a table then typing `.` must still open the
		// column popup. Non-dot keystrokes stay suppressed so the just-
		// inserted identifier doesn't re-pop the table list.
		if !editor.IsIdentDotContext(buf, pos) {
			return
		}
	}
	if !c.suggestions.IsVisible() && !editor.AutoTriggerFromContext(buf, pos) {
		return
	}
	c.RefilterOrTrigger(buf, pos)
}

// isClipboardRegister reports whether reg maps to the OS clipboard under
// unnamedplus semantics. reg == 0 defaults to '"' (the unnamed register)
// first, so the unnamed/default register and the '+'/'*' registers all
// mirror to the clipboard; named registers a–z stay internal-only.
func isClipboardRegister(reg rune) bool {
	if reg == 0 {
		reg = '"'
	}
	return reg == '"' || reg == '+' || reg == '*'
}

// mirrorYankToClipboard pushes yanked text to the OS clipboard under
// unnamedplus semantics. Fires only for the yank operator targeting a
// clipboard register; deletes/changes never reach here (they pass a
// non-yank actionID). Best-effort: a Write error is swallowed because the
// internal register is already authoritative.
func (c *VimEditorController) mirrorYankToClipboard(actionID string, reg rune, text string) {
	if c.clipboard == nil {
		return
	}
	if actionID != commands.OperatorYank {
		return
	}
	if !isClipboardRegister(reg) {
		return
	}
	if err := c.clipboard.Write(text); err != nil {
		return
	}
	c.lastWrite = text
}

// motionFunc is the shared signature every motion in pkg/gui/editor
// implements: pure (no Buffer mutation), pre-validated by the caller.
// frame carries the editor viewport for the view-relative motions
// (H/M/L); all other motions ignore it.
type motionFunc func(b *editor.Buffer, pos editor.Position, count int, frame editor.ViewFrame) (editor.Position, bool)

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
// {, } per the wwd architecture decisions).
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

// operatorModeMask is the NON-Normal Mode mask under which operator
// bindings fire. ModeNormal (zero sentinel) cannot be ORed into a
// bitmask — it vanishes — so Normal-mode entries are registered
// separately in GetKeybindings.
const operatorModeMask = types.ModeOperatorPending |
	types.ModeVisual | types.ModeVisualLine | types.ModeVisualBlock

// motionModeMask is the NON-Normal Mode mask under which motion
// bindings fire. ModeNormal is registered separately in GetKeybindings.
const motionModeMask = types.ModeOperatorPending |
	types.ModeVisual | types.ModeVisualLine | types.ModeVisualBlock

// textObjectModeMask is the Mode mask under which text-object bindings
// fire. OperatorPending is the original (wwd.6); wwd.7 extends to
// Visual / VisualLine so `vi"`-style flows snap the Selection to the
// resolved object. ModeVisualBlock is included (dbsavvy-uly7.9) so
// `<c-v>iw`-style flows snap the Selection too.
const textObjectModeMask = types.ModeOperatorPending |
	types.ModeVisual | types.ModeVisualLine | types.ModeVisualBlock

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

// pasteModeMask is the NON-Normal Mode mask under which `p` (paste)
// fires — the visual modes, where `p` replaces the selection with the
// register contents. Normal-mode `p` is registered separately (the zero
// sentinel ModeNormal cannot be ORed into a bitmask).
const pasteModeMask = types.ModeVisual | types.ModeVisualLine | types.ModeVisualBlock

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
	out := make([]*types.ChordBinding, 0, 2*len(specs)+len(textObjects)+len(visuals)+len(inserts)+len(histories)+2*len(operators)+2)
	for _, s := range specs {
		seq, err := keys.SequenceFromShorthand(s.shorthand)
		if err != nil {
			continue
		}
		for _, mode := range []types.Mode{types.ModeNormal, motionModeMask} {
			out = append(out, &types.ChordBinding{
				Sequence:    seq,
				Mode:        mode,
				Scope:       types.QUERY_EDITOR,
				ActionID:    s.actionID,
				Description: s.description,
				Tag:         "Motion",
			})
		}
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
	// Paste binding: `p` in Normal (paste after cursor) and in the
	// visual modes (replace the selection with the register contents).
	// ModeNormal is the zero sentinel so it is registered separately
	// from the visual mask.
	if seq, err := keys.SequenceFromShorthand("p"); err == nil {
		for _, mode := range []types.Mode{types.ModeNormal, pasteModeMask} {
			out = append(out, &types.ChordBinding{
				Sequence:    seq,
				Mode:        mode,
				Scope:       types.QUERY_EDITOR,
				ActionID:    commands.EditorPaste,
				Description: "paste after cursor",
				Tag:         "Edit",
			})
		}
	}
	// Paste-before binding: `P` mirrors `p` but inserts before the
	// cursor (char-wise) / above the line (line-wise). Same Normal +
	// visual mask split as `p`.
	if seq, err := keys.SequenceFromShorthand("P"); err == nil {
		for _, mode := range []types.Mode{types.ModeNormal, pasteModeMask} {
			out = append(out, &types.ChordBinding{
				Sequence:    seq,
				Mode:        mode,
				Scope:       types.QUERY_EDITOR,
				ActionID:    commands.EditorPasteBefore,
				Description: "paste before cursor",
				Tag:         "Edit",
			})
		}
	}
	// Toggle-case binding: `~` in Normal + visual. With tildeop off
	// (default) `~` is NOT an operator — it acts immediately, so it
	// is bound standalone here rather than via operatorSpecs.
	if seq, err := keys.SequenceFromShorthand("~"); err == nil {
		for _, mode := range []types.Mode{types.ModeNormal, pasteModeMask} {
			out = append(out, &types.ChordBinding{
				Sequence:    seq,
				Mode:        mode,
				Scope:       types.QUERY_EDITOR,
				ActionID:    commands.EditorToggleCase,
				Description: "toggle case",
				Tag:         "Edit",
			})
		}
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
	// `D` — delete to end of line (vim `d$` alias). Normal-only single
	// keystroke (dbsavvy-5fxk).
	if seq, err := keys.SequenceFromShorthand("D"); err == nil {
		out = append(out, &types.ChordBinding{
			Sequence:    seq,
			Mode:        types.ModeNormal,
			Scope:       types.QUERY_EDITOR,
			ActionID:    commands.OperatorDeleteEndOfLine,
			Description: "delete to end of line",
			Tag:         "Operator",
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
		for _, mode := range []types.Mode{types.ModeNormal, operatorModeMask} {
			out = append(out, &types.ChordBinding{
				Sequence:    seq,
				Mode:        mode,
				Scope:       types.QUERY_EDITOR,
				ActionID:    s.actionID,
				Description: s.description,
				Tag:         "Operator",
			})
		}
	}
	// dbsavvy-bwq.Z1: `<c-x><c-o>` triggers the completion engine. Insert-
	// only mode mask so the chord doesn't shadow Normal-mode bindings.
	if seq, err := keys.SequenceFromShorthand("<c-x><c-o>"); err == nil {
		out = append(out, &types.ChordBinding{
			Sequence:    seq,
			Mode:        types.ModeInsert,
			Scope:       types.QUERY_EDITOR,
			ActionID:    commands.EditorCompletionTrigger,
			Description: "Trigger completion",
			Tag:         "Insert",
		})
	}
	// dbsavvy-etp.1: Insert-mode popup-navigation aliases. These keys are
	// otherwise dropped by the insert seam, so each handler guards on
	// suggestions.IsVisible() and no-ops when the popup is hidden —
	// preserving the keys' (currently inert) Insert behaviour.
	for _, s := range c.completionNavSpecs() {
		if seq, err := keys.SequenceFromShorthand(s.shorthand); err == nil {
			out = append(out, &types.ChordBinding{
				Sequence:    seq,
				Mode:        types.ModeInsert,
				Scope:       types.QUERY_EDITOR,
				ActionID:    s.actionID,
				Description: s.description,
				Tag:         "Insert",
			})
		}
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
		// `jk` exits Insert like <esc> (Insert-only: in OperatorPending j/k
		// stay motions). A lone `j` flushes as literal text on the chord
		// timeout / when broken by a non-`k` key — see keys.Matcher.
		{"jk", commands.ModeNormal, "exit insert (jk)", "Insert", types.ModeInsert},
	}
}

// completionNavSpecs returns the etp.1 Insert-mode popup-navigation
// aliases. Tab/Enter are handled in the VimEditor insert seam (they
// keep their normal Insert meaning when the popup is hidden); the Vim
// control aliases below are registered as Insert bindings because the
// insert seam otherwise drops them. Each handler guards on
// suggestions.IsVisible().
func (c *VimEditorController) completionNavSpecs() []editorActionSpec {
	return []editorActionSpec{
		{"<c-n>", commands.EditorCompletionNext, "next suggestion", "Insert", types.ModeInsert},
		{"<c-p>", commands.EditorCompletionPrev, "prev suggestion", "Insert", types.ModeInsert},
		{"<c-y>", commands.EditorCompletionAccept, "accept suggestion", "Insert", types.ModeInsert},
		{"<c-e>", commands.EditorCompletionDismiss, "dismiss suggestions", "Insert", types.ModeInsert},
	}
}

// editorHistorySpecs returns the wwd.10 undo / redo bindings.
func (c *VimEditorController) editorHistorySpecs() []editorActionSpec {
	return []editorActionSpec{
		{"u", commands.EditorUndo, "undo", "Edit history", editorHistoryModeMask},
		{"<c-r>", commands.EditorRedo, "redo", "Edit history", editorHistoryModeMask},
		{"g+", commands.EditorRedo, "redo (g+)", "Edit history", editorHistoryModeMask},
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
		ID:          commands.EditorPasteBefore,
		Description: "Paste before cursor",
		Tag:         "Edit",
		Handler:     c.pasteBeforeHandler(),
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.EditorToggleCase,
		Description: "Toggle case under cursor / over selection",
		Tag:         "Edit",
		Handler:     c.toggleCaseHandler(),
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.OperatorDeleteEndOfLine,
		Description: "Delete to end of line",
		Tag:         "Operator",
		Handler:     c.deleteEndOfLineHandler(),
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.EditorRepeat,
		Description: "Repeat last edit (vim `.`)",
		Tag:         "Edit history",
		Handler:     c.repeatHandler(),
	})
	// dbsavvy-bwq.Z1: `<c-x><c-o>` completion trigger. TriggerCompletion
	// is a silent no-op when the engine / suggestions context are unwired,
	// so the binding is safe to register before either lands.
	_ = reg.Register(&commands.Command{
		ID:          commands.EditorCompletionTrigger,
		Description: "Trigger completion",
		Tag:         "Insert",
		Handler: func(_ commands.ExecCtx) error {
			return c.TriggerCompletion()
		},
	})
	// dbsavvy-etp.1: popup-navigation handlers. Each guards on
	// suggestions.IsVisible() and is a no-op when the popup is hidden.
	_ = reg.Register(&commands.Command{
		ID:          commands.EditorCompletionNext,
		Description: "Next suggestion",
		Tag:         "Insert",
		Handler:     c.completionNextHandler(),
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.EditorCompletionPrev,
		Description: "Previous suggestion",
		Tag:         "Insert",
		Handler:     c.completionPrevHandler(),
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.EditorCompletionAccept,
		Description: "Accept suggestion",
		Tag:         "Insert",
		Handler:     c.completionAcceptHandler(),
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.EditorCompletionDismiss,
		Description: "Dismiss suggestions",
		Tag:         "Insert",
		Handler:     c.completionDismissHandler(),
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
			newPos, motionOK := ms.fn(buf, from, replayCount, c.viewFrame())
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
		{"iw", commands.TextObjectInnerWord, "inside word", editor.InnerWord},
		{"aw", commands.TextObjectAroundWord, "around word", editor.AroundWord},
		{"iW", commands.TextObjectInnerWORD, "inside WORD", editor.InnerWORD},
		{"aW", commands.TextObjectAroundWORD, "around WORD", editor.AroundWORD},
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
		if ec.Mode.Has(types.ModeVisual | types.ModeVisualLine | types.ModeVisualBlock) {
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
		newPos, ok := spec.fn(buf, from, count, c.viewFrame())
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
		c.mirrorYankToClipboard(spec.actionID, ctx.Register, capture)
		// r is already half-open (op-pending motion range); flash as-is.
		c.flashYank(spec.actionID, buf, r)
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

// deleteEndOfLineHandler returns the `D` action handler — vim's single-
// key alias for `d$`. It stashes the delete operator and completes it
// through applyPending with a cursor→line-end range, reusing the
// register-write + `.`-repeat bookkeeping the motion-driven `d$` path
// already owns. Normal-mode only; a cursor already at end-of-line yields
// a zero-length range, so applyPending runs the delete as a silent no-op
// and returns to Normal cleanly.
func (c *VimEditorController) deleteEndOfLineHandler() commands.Handler {
	return func(ec commands.ExecCtx) error {
		buf := c.buffer()
		if buf == nil || c.qec == nil {
			return nil
		}
		rep := c.qec.Repeat()
		if rep == nil {
			return nil
		}
		from := buf.CursorPos()
		end, ok := editor.LineEnd(buf, from, 1, editor.ViewFrame{})
		if !ok {
			end = from
		}
		rep.PendingOpID = commands.OperatorDelete
		return c.applyPending(buf, editor.Range{Start: from, End: end}, commands.MotionLineEnd, "", ec)
	}
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
		c.mirrorYankToClipboard(spec.actionID, ec.Register, capture)
		// Flash exactly the span Yank consumed. In this codebase the
		// post-NormaliseRange `sel` is ALREADY half-open: TextInRange slices
		// [Start.Col:End.Col), so the same `sel` passed to spec.apply tints
		// precisely the yanked text. NO endCol++ here (that would tint one
		// trailing column past the yank). Verified: visual `y` over cols
		// [0,0]→[0,5] on "hello world" yanks "hello" and sel.End.Col==5, so
		// half-open [0,5) is correct. Use sel captured before ExitVisual.
		c.flashYank(spec.actionID, buf, sel)
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
		c.mirrorYankToClipboard(spec.actionID, ec.Register, capture)
		// r is LineWise (yy); flash tints whole lines, no col change.
		c.flashYank(spec.actionID, buf, r)
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
//
// For clipboard registers ('"' / '+' / '*'), an external clipboard change
// wins (unnamedplus): when the OS clipboard reads cleanly, is non-empty,
// and differs from the text we last mirrored, that content is returned
// char-wise. Otherwise (no clipboard wired, read error, empty clipboard,
// or clipboard == our own lastWrite) the in-memory register is used.
//
// ACCEPTED line-wise regression (decision 4): some backends strip a
// trailing newline on read (macOS pbpaste, some Wayland), so cb !=
// lastWrite even for our own line-wise yank — there yyp/ddp degrade to a
// char-wise mid-line paste. Verified faithful on Linux/xclip; documented,
// not tested.
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
	if c.clipboard != nil && isClipboardRegister(reg) {
		if cb, err := c.clipboard.Read(); err == nil && cb != "" && cb != c.lastWrite {
			return cb // external change wins; char-wise
		}
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
// Empty register is a no-op. For clipboard registers ('"' / '+' / '*')
// readRegister consults the OS clipboard first (unnamedplus); see its doc.
func (c *VimEditorController) pasteHandler() commands.Handler {
	return func(ec commands.ExecCtx) error {
		buf := c.buffer()
		if buf == nil {
			return nil
		}
		if ec.Mode.Has(types.ModeVisual | types.ModeVisualLine | types.ModeVisualBlock) {
			return c.visualPaste(buf, ec)
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

// visualPaste implements `p` in the visual modes: the selection is
// deleted and the effective register's contents are put in its place,
// then Visual is exited back to Normal. An empty register leaves the
// buffer unchanged. The deleted text is intentionally NOT
// written back to any register — vim clobbers the unnamed register
// here, but preserving it lets the same yank replace several
// selections in turn, which is the more useful behaviour.
func (c *VimEditorController) visualPaste(buf *editor.Buffer, ec commands.ExecCtx) error {
	if buf.Selection == nil {
		editor.ExitVisual(buf)
		c.setMode(types.ModeNormal)
		return nil
	}
	text := c.readRegister(ec.Register)
	if text == "" {
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
	if _, err := editor.Delete(buf, sel); err != nil {
		return err
	}
	if err := c.putAt(buf, sel.Start, text); err != nil {
		return err
	}
	editor.ExitVisual(buf)
	c.setMode(types.ModeNormal)
	return nil
}

// pasteBeforeHandler implements vim `P`: like pasteHandler but the
// register lands before the cursor (char-wise) or above the current
// line (line-wise). Visual mode delegates to visualPaste, identical to
// `p`. An empty register is a no-op.
func (c *VimEditorController) pasteBeforeHandler() commands.Handler {
	return func(ec commands.ExecCtx) error {
		buf := c.buffer()
		if buf == nil {
			return nil
		}
		if ec.Mode.Has(types.ModeVisual | types.ModeVisualLine | types.ModeVisualBlock) {
			return c.visualPaste(buf, ec)
		}
		text := c.readRegister(ec.Register)
		if text == "" {
			return nil
		}
		cur := buf.CursorPos()
		// Line-wise: insert body + "\n" at col 0 of the current line so
		// the pasted lines land ABOVE it; the cursor stays on col 0,
		// which is now the first pasted line.
		if text[len(text)-1] == '\n' {
			body := text[:len(text)-1]
			pos := editor.Position{Line: cur.Line, Col: 0}
			if err := buf.Apply(editor.Edit{
				Kind:  editor.EditKindInsert,
				Range: editor.Range{Start: pos, End: pos},
				Text:  body + "\n",
			}); err != nil {
				return err
			}
			buf.SetCursor(editor.Position{Line: cur.Line, Col: 0})
			return nil
		}
		// Char-wise: insert before the cursor (vim `P`).
		pos := editor.Position{Line: cur.Line, Col: cur.Col}
		return buf.Apply(editor.Edit{
			Kind:  editor.EditKindInsert,
			Range: editor.Range{Start: pos, End: pos},
			Text:  text,
		})
	}
}

// toggleCaseHandler implements vim `~` with tildeop off: in Normal mode
// it flips the case of `count` chars starting under the cursor and
// advances; in visual mode it flips the selection and exits to Normal.
// Non-letters are left unchanged but the cursor still advances.
func (c *VimEditorController) toggleCaseHandler() commands.Handler {
	return func(ec commands.ExecCtx) error {
		buf := c.buffer()
		if buf == nil {
			return nil
		}
		if ec.Mode.Has(types.ModeVisual | types.ModeVisualLine | types.ModeVisualBlock) {
			return c.toggleCaseVisual(buf, ec)
		}
		cur := buf.CursorPos()
		lineLen := buf.LineRuneLen(cur.Line)
		if lineLen == 0 {
			return nil
		}
		count := ec.Count
		if count <= 0 {
			count = 1
		}
		end := min(cur.Col+count, lineLen)
		if err := editor.ToggleCase(buf, editor.Range{
			Start: cur,
			End:   editor.Position{Line: cur.Line, Col: end},
		}); err != nil {
			return err
		}
		buf.SetCursor(editor.Position{Line: cur.Line, Col: min(end, lineLen-1)})
		return nil
	}
}

// toggleCaseVisual flips the case of the active selection and exits
// visual mode. No register write or flash — `~` is a pure in-place
// mutation. The cursor lands at the selection start via ExitVisual.
func (c *VimEditorController) toggleCaseVisual(buf *editor.Buffer, ec commands.ExecCtx) error {
	if buf.Selection == nil {
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
	if err := editor.ToggleCase(buf, sel); err != nil {
		return err
	}
	editor.ExitVisual(buf)
	c.setMode(types.ModeNormal)
	return nil
}

// putAt inserts register text at the position left by a visual-mode
// delete. Line-wise register contents (trailing '\n') become whole
// lines at start.Line — or are appended below the last line when the
// delete consumed the buffer's tail. Char-wise contents are inserted
// inline at start.
func (c *VimEditorController) putAt(buf *editor.Buffer, start editor.Position, text string) error {
	if text[len(text)-1] == '\n' {
		body := text[:len(text)-1]
		if start.Line < buf.LineCount() {
			pos := editor.Position{Line: start.Line, Col: 0}
			if err := buf.Apply(editor.Edit{
				Kind:  editor.EditKindInsert,
				Range: editor.Range{Start: pos, End: pos},
				Text:  body + "\n",
			}); err != nil {
				return err
			}
			buf.SetCursor(editor.Position{Line: start.Line, Col: 0})
			return nil
		}
		last := buf.LineCount() - 1
		pos := editor.Position{Line: last, Col: buf.LineRuneLen(last)}
		if err := buf.Apply(editor.Edit{
			Kind:  editor.EditKindInsert,
			Range: editor.Range{Start: pos, End: pos},
			Text:  "\n" + body,
		}); err != nil {
			return err
		}
		buf.SetCursor(editor.Position{Line: last + 1, Col: 0})
		return nil
	}
	pos := start
	if maxCol := buf.LineRuneLen(pos.Line); pos.Col > maxCol {
		pos.Col = maxCol
	}
	if err := buf.Apply(editor.Edit{
		Kind:  editor.EditKindInsert,
		Range: editor.Range{Start: pos, End: pos},
		Text:  text,
	}); err != nil {
		return err
	}
	buf.SetCursor(pos)
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

// viewFrame returns the current editor viewport for the view-relative
// motions (H/M/L). A nil qec or unwired view yields the zero ViewFrame,
// which the motions treat as "viewport unavailable" (buffer-relative
// fallback).
func (c *VimEditorController) viewFrame() editor.ViewFrame {
	if c.qec == nil {
		return editor.ViewFrame{}
	}
	return c.qec.ViewFrame()
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
		if next, ok := editor.LineFirstNonBlank(buf, from, 1, editor.ViewFrame{}); ok {
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

// completionNextHandler returns the `<c-n>` handler: advance the popup
// selection when visible, otherwise a no-op (the key is otherwise
// dropped by the insert seam). etp.1.
func (c *VimEditorController) completionNextHandler() commands.Handler {
	return func(_ commands.ExecCtx) error {
		if c.suggestions != nil && c.suggestions.IsVisible() {
			c.suggestions.Next()
		}
		return nil
	}
}

// completionPrevHandler returns the `<c-p>` handler: retreat the popup
// selection when visible, otherwise a no-op. etp.1.
func (c *VimEditorController) completionPrevHandler() commands.Handler {
	return func(_ commands.ExecCtx) error {
		if c.suggestions != nil && c.suggestions.IsVisible() {
			c.suggestions.Prev()
		}
		return nil
	}
}

// completionAcceptHandler returns the `<c-y>` handler: accept the
// selected suggestion (replace the typed partial identifier) when
// visible, otherwise a no-op. etp.1.
func (c *VimEditorController) completionAcceptHandler() commands.Handler {
	return func(_ commands.ExecCtx) error {
		c.acceptSuggestion()
		return nil
	}
}

// completionDismissHandler returns the `<c-e>` handler: hide the popup
// when visible (staying in Insert mode), otherwise a no-op. etp.1.
func (c *VimEditorController) completionDismissHandler() commands.Handler {
	return func(_ commands.ExecCtx) error {
		if c.suggestions != nil && c.suggestions.IsVisible() {
			c.suggestions.Hide()
		}
		return nil
	}
}

// CompletionKey is the seam VimEditor.SetCompletionKey consults at the
// top of the insert path for Tab / Shift+Tab / Enter. It returns true
// only when the popup is visible (i.e. it consumed the key): Tab advances
// the selection, Shift+Tab (Backtab) moves it backward, Enter accepts.
// When the popup is hidden it returns false so the key keeps its normal
// Insert meaning (Tab dropped, Enter newline). etp.1.
func (c *VimEditorController) CompletionKey(k keys.Key) bool {
	if c.suggestions == nil || !c.suggestions.IsVisible() {
		return false
	}
	switch k.Special {
	case keys.KeyTab:
		c.suggestions.Next()
		return true
	case keys.KeyBacktab:
		c.suggestions.Prev()
		return true
	case keys.KeyEnter:
		c.acceptSuggestion()
		return true
	}
	return false
}

// acceptSuggestion replaces the partial identifier under the cursor with
// the selected candidate (delete the typed prefix, insert the full
// suggestion). The identifier range is recomputed from the LIVE buffer
// line (scan back over identifier runes from the cursor); the stored
// anchor is used only to detect staleness: if the cursor has left the
// identifier the popup tracked (different line, or before the
// identifier start), the buffer mutation is aborted and the popup is
// dismissed instead of corrupting text. No-op when the popup is hidden.
// etp.1.
func (c *VimEditorController) acceptSuggestion() {
	if c.suggestions == nil || !c.suggestions.IsVisible() {
		return
	}
	buf := c.buffer()
	if buf == nil {
		c.suggestions.Hide()
		return
	}
	anchor := c.suggestions.Anchor()
	cur := buf.CursorPos()
	identStart := identifierStartCol(buf, cur)
	// Stale-anchor guard. anchor marks the cursor at trigger time (the
	// end of the typed prefix). A valid accept requires the cursor still
	// sit on the anchor line, at or past the trigger column (the cursor
	// only ever advances by typing — retreating means the prefix was
	// deleted out from under the popup), and the live identifier run must
	// still extend back through the trigger point (identStart <=
	// anchor.Col). Otherwise the replace would corrupt unrelated text —
	// abort + dismiss.
	if cur.Line != anchor.Line || cur.Col < anchor.Col || identStart > anchor.Col {
		c.suggestions.Hide()
		return
	}
	s, ok := c.suggestions.Accept()
	if !ok {
		return
	}
	// dbsavvy-ko4m.7.2: a snippet accept inserts the (already
	// SanitizeSnippetText'd) Body as ONE undoable multi-line edit. Replace
	// the typed partial [identStart, cur) with the Body via a single
	// EditKindReplace (one undo node even for a multi-line body — Apply
	// records exactly one reversible Edit), then land the cursor at
	// EndOfInsert(at, body) so a multi-line body leaves the cursor on the
	// final chunk, not at identStart+len(body). This short-circuits the
	// alias/qualify smart-accept branches below — a snippet body is not a
	// bare table/column identifier.
	if s.Kind == editor.KindSnippet {
		at := editor.Position{Line: cur.Line, Col: identStart}
		if err := buf.Apply(editor.Edit{
			Kind:  editor.EditKindReplace,
			Range: editor.Range{Start: at, End: cur},
			Text:  s.Body,
		}); err != nil {
			return
		}
		buf.SetCursor(editor.EndOfInsert(at, s.Body))
		c.suppressNextAutoTrigger = true
		return
	}
	// dbsavvy-ko4m.6.2 / .6.3: accept-time context analysis drives two
	// SINGLE-EditKindReplace smart-accept branches (one undo step each):
	//   - Expect==Tables → auto-insert an editable deduped alias ("<table> <alias>").
	//   - Expect==Columns → auto-qualify an ambiguous column ("<alias>.<column>").
	// The plain (non-smart) path keeps its Delete+Insert shape untouched.
	ctx := editor.AnalyzeContextAt(buf, cur)
	if c.aliasOnAccept && ctx.Expect == sqlcontext.ExpectTables {
		text := composeTableAlias(s.Text, ctx.InScopeTables)
		if err := buf.Apply(editor.Edit{
			Kind:  editor.EditKindReplace,
			Range: editor.Range{Start: editor.Position{Line: cur.Line, Col: identStart}, End: cur},
			Text:  text,
		}); err != nil {
			return
		}
		buf.SetCursor(editor.Position{Line: cur.Line, Col: identStart + len([]rune(text))})
		c.suppressNextAutoTrigger = true
		return
	}
	// dbsavvy-ko4m.6.3: in a column context (Expect==Columns) that is NOT
	// already dot-qualified, auto-qualify the accepted column with the
	// first owning in-scope table's alias when the column is AMBIGUOUS —
	// it appears in >=2 in-scope tables AND every consulted table is warmed.
	// composeColumnQualifier returns the bare column ("<column>") when not
	// ambiguous / unwarmed / no metadata / empty scope, so the single
	// EditKindReplace below is uniform either way.
	//
	// "Already dot-qualified" is detected two ways: ctx.Qualifier.Present
	// (cursor sits right after `alias.` with no partial), AND a dot
	// immediately left of the live identifier run (the partial-after-dot
	// case `alias.par|`, which the engine does not flag as a Qualifier
	// because the dot no longer ends at the cursor). Either way the user has
	// already chosen the table, so re-qualifying would emit `alias.alias.col`.
	if ctx.Expect == sqlcontext.ExpectColumns && !ctx.Qualifier.Present && !dotPrecedesIdentStart(buf, editor.Position{Line: cur.Line, Col: identStart}) {
		text := c.composeColumnQualifier(s.Text, ctx.InScopeTables)
		if err := buf.Apply(editor.Edit{
			Kind:  editor.EditKindReplace,
			Range: editor.Range{Start: editor.Position{Line: cur.Line, Col: identStart}, End: cur},
			Text:  text,
		}); err != nil {
			return
		}
		buf.SetCursor(editor.Position{Line: cur.Line, Col: identStart + len([]rune(text))})
		c.suppressNextAutoTrigger = true
		return
	}
	// Delete [identStart, cur) then insert the candidate at identStart.
	if identStart < cur.Col {
		if err := buf.Apply(editor.Edit{
			Kind:  editor.EditKindDelete,
			Range: editor.Range{Start: editor.Position{Line: cur.Line, Col: identStart}, End: cur},
		}); err != nil {
			return
		}
	}
	at := editor.Position{Line: cur.Line, Col: identStart}
	if err := buf.Apply(editor.Edit{
		Kind:  editor.EditKindInsert,
		Range: editor.Range{Start: at, End: at},
		Text:  s.Text,
	}); err != nil {
		return
	}
	buf.SetCursor(editor.Position{Line: cur.Line, Col: identStart + len([]rune(s.Text))})
	// Re-popup-after-accept guard (dbsavvy-etp.4): the inserted identifier
	// can still satisfy the AutoTriggerFromContext gate, so suppress the
	// next as-you-type trigger and require a fresh keystroke.
	c.suppressNextAutoTrigger = true
}

// composeTableAlias builds the "<table> <alias>" text inserted on a
// table-context accept (dbsavvy-ko4m.6.2). The alias is the first letter
// of the table name lowercased, deduped against the aliases already bound
// by inScope by appending a numeric suffix (u, u2, u3, …; suffixes already
// present are skipped). Both the table token and the alias are emitted in
// their SQL-safe form (double-quoted when they would not round-trip bare)
// so the inserted text is valid SQL (Finding Q). The table name fed in is
// the raw candidate text; aliasBase derives off its unquoted form.
func composeTableAlias(table string, inScope []sqlcontext.TableRef) string {
	base := bareIdent(table)
	alias := dedupedAlias(base, inScope)
	return quoteIfNeeded(table) + " " + quoteIfNeeded(alias)
}

// composeColumnQualifier builds the text inserted on a column-context
// accept (dbsavvy-ko4m.6.3). It returns the AUTO-QUALIFIED form
// `"<alias>.<column>"` only when the accepted column is AMBIGUOUS — it
// belongs to >=2 in-scope tables AND every consulted table's columns are
// warmed (Store.Columns ok==true) — using the FIRST owning in-scope table's
// alias (by InScopeTables order). Otherwise it returns the BARE column
// (`"<column>"`): <2 owners, any consulted table not warmed, no metadata
// reader, or empty scope. Both the alias and column are emitted SQL-safe via
// quoteIfNeeded. The "first owning" table must have a non-empty Alias to be
// usable as a qualifier; a column owned by an unaliased table still counts
// toward ambiguity but cannot itself supply the qualifier — the first owner
// WITH an alias wins.
func (c *VimEditorController) composeColumnQualifier(column string, inScope []sqlcontext.TableRef) string {
	if c.schemaMeta == nil || len(inScope) == 0 {
		return quoteIfNeeded(column)
	}
	name := bareIdent(column)
	owners := 0
	firstAliased := ""
	for _, t := range inScope {
		if t.Name == "" {
			continue
		}
		cols, ok := c.schemaMeta.Columns(c.schemaFor(t.Schema), t.Name)
		if !ok {
			// Unwarmed consulted table — cannot be sure of ambiguity, so
			// never guess: insert the bare column.
			return quoteIfNeeded(column)
		}
		if !columnsContain(cols, name) {
			continue
		}
		owners++
		if firstAliased == "" && t.Alias != "" {
			firstAliased = t.Alias
		}
	}
	if owners < 2 || firstAliased == "" {
		return quoteIfNeeded(column)
	}
	return quoteIfNeeded(firstAliased) + "." + quoteIfNeeded(column)
}

// schemaFor resolves the schema for an in-scope table reference: the
// reference's own schema qualifier when present, else the active schema from
// the wired provider (mirrors editor.SchemaSource.schemaFor). Nil provider
// yields "". dbsavvy-ko4m.6.3.
func (c *VimEditorController) schemaFor(refSchema string) string {
	if refSchema != "" {
		return refSchema
	}
	if c.schemaProv == nil {
		return ""
	}
	return c.schemaProv()
}

// columnsContain reports whether cols holds a column whose bare name equals
// name, case-insensitively (Postgres folds unquoted identifiers to
// lowercase; the snapshot stores names as the catalog reports them). The
// accepted candidate name is the bare (unquoted) form. dbsavvy-ko4m.6.3.
func columnsContain(cols []models.Column, name string) bool {
	for _, col := range cols {
		if strings.EqualFold(col.Name, name) {
			return true
		}
	}
	return false
}

// dedupedAlias derives the alias (first rune of the table's bare name,
// lowercased) and resolves collisions against the aliases already in
// scope by numeric suffix: the bare letter first, then letter+2,
// letter+3, … skipping any already taken. An empty base yields no usable
// first letter, so the alias falls back to "a". Comparison is
// case-insensitive on the unquoted alias text.
func dedupedAlias(base string, inScope []sqlcontext.TableRef) string {
	letter := "a"
	for _, r := range base {
		letter = string(unicode.ToLower(r))
		break
	}
	taken := make(map[string]bool, len(inScope))
	for _, t := range inScope {
		if t.Alias != "" {
			taken[strings.ToLower(t.Alias)] = true
		}
	}
	if !taken[letter] {
		return letter
	}
	for n := 2; ; n++ {
		cand := letter + strconv.Itoa(n)
		if !taken[cand] {
			return cand
		}
	}
}

// bareIdent strips a surrounding pair of double quotes from a candidate
// identifier so alias derivation works off the unquoted name
// (`"MyTable"` -> `MyTable`). Unquoted input is returned unchanged.
func bareIdent(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return strings.ReplaceAll(s[1:len(s)-1], `""`, `"`)
	}
	return s
}

// quoteIfNeeded returns s double-quoted when it would not round-trip as a
// bare SQL identifier — i.e. it is already quoted, empty, contains a rune
// outside [a-z0-9_], or differs from its lowercased form (mixed-case)
// (Finding Q). An already-quoted token is passed through unchanged. A bare
// all-lowercase/digit/underscore identifier is returned as-is.
func quoteIfNeeded(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s
	}
	if s == "" || s != strings.ToLower(s) || !isBareIdent(s) {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

// isBareIdent reports whether every rune of s lies in [a-z0-9_]; an empty
// string is not a bare identifier.
func isBareIdent(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

// identifierStartCol returns the column of the first identifier rune of
// the run immediately left of pos on pos.Line, or pos.Col when no
// identifier precedes the cursor. Mirrors editor.identifierPrefixAt's
// scan (letters / digits / underscore). etp.1.
func identifierStartCol(buf *editor.Buffer, pos editor.Position) int {
	lines := buf.LinesCopy()
	if pos.Line < 0 || pos.Line >= len(lines) {
		return pos.Col
	}
	runes := lines[pos.Line].Runes
	end := min(pos.Col, len(runes))
	start := end
	for start > 0 && isIdentRune(runes[start-1]) {
		start--
	}
	return start
}

// dotPrecedesIdentStart reports whether the rune immediately left of pos on
// pos.Line is a '.', i.e. the identifier run starting at pos is the member
// part of a dot-qualified reference (`alias.col`). Used by the column
// auto-qualify accept path to avoid double-qualifying a column the user has
// already qualified with an explicit `alias.` prefix (dbsavvy-ko4m.6.3).
func dotPrecedesIdentStart(buf *editor.Buffer, pos editor.Position) bool {
	lines := buf.LinesCopy()
	if pos.Line < 0 || pos.Line >= len(lines) {
		return false
	}
	runes := lines[pos.Line].Runes
	if pos.Col <= 0 || pos.Col > len(runes) {
		return false
	}
	return runes[pos.Col-1] == '.'
}

// isIdentRune reports whether r is part of a SQL identifier (letters,
// digits, underscore). Mirrors editor.isIdentRune, duplicated here
// because that helper is unexported in pkg/gui/editor. etp.1.
func isIdentRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
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
		// The exit-insert action (any key bound to it: <esc>, jk, …) closes
		// the completion popup AND drops to Normal in a single press. We
		// dismiss the popup first, then fall through to the normal exit so
		// it never strands the user in Insert (cf. stuck-in-insert
		// regression, commit f8a6452).
		if c.suggestions != nil && c.suggestions.IsVisible() {
			c.suggestions.Hide()
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
