package commands

// Action IDs.
//
// These string constants are the stable contract between:
//
//   - pkg/gui/controllers           (publish bindings citing these IDs)
//   - pkg/config                    (validates `action: …` strings in user config)
//   - pkg/gui/keys                  (resolves IDs to Handlers via the Registry)
//   - pkg/cheatsheet                (renders by ID)
//
// IDs are dot-namespaced (`namespace.verb` or `namespace.subnamespace.verb`).
// A new ID is added here BEFORE any controller registers it.
//
// Sources:
//   - DESIGN.md §10.10 config schema example
//   - Existing E1–E4 controller publishings (pre-dlp.8 KeyBinding lists)
//
// Out of scope for dlp.1: query/result/cursor/pane families (added by
// later epics dbsavvy-66p, dbsavvy-wwd, etc.). dlp.7 adds `:reload`
// via the ExRegistry, not the CommandRegistry — so no `reload.config`
// constant appears here.
//
// Namespace ownership:
//
//   - The `motion.*`, `operator.*`, and `textobject.*` namespaces are
//     owned exclusively by VimEditorController (epic dbsavvy-wwd). No
//     other controller may register an ID in these namespaces. Action
//     IDs in those namespaces resolve through the same Registry but
//     their handlers are constructed by VimEditorController with closures
//     over the live *editor.Buffer of the focused QUERY_EDITOR pane.
const (
	// AppQuit — owned by QuitController. Maps to `<leader>q` by default.
	AppQuit = "app.quit"

	// SchemaHide / SchemaUnhide / SchemaToggleShowHidden — owned by
	// SchemasController. Mapped to `H`, `U`, `<leader>H` respectively.
	SchemaHide             = "schema.hide"
	SchemaUnhide           = "schema.unhide"
	SchemaToggleShowHidden = "schema.toggle_show_hidden"

	// ConnectionAdd — owned by ConnectionsController. Maps to `a`.
	ConnectionAdd = "connection.add"

	// ListUp / ListDown / ListConfirm — published by ListControllerTrait
	// (shared by every side-rail). `j`/`k`/`<cr>`.
	ListUp      = "list.up"
	ListDown    = "list.down"
	ListConfirm = "list.confirm"

	// Rail-switch family — published by every side-rail controller via
	// railSwitchBindings (pkg/gui/controllers/shared.go). Digits 1..4
	// jump to a specific rail; `<tab>` cycles to the next rail.
	RailSwitchSchemas = "rail.switch.schemas"
	RailSwitchTables  = "rail.switch.tables"
	RailSwitchColumns = "rail.switch.columns"
	RailSwitchIndexes = "rail.switch.indexes"
	RailSwitchNext    = "rail.switch.next"

	// MenuConfirm / MenuCancel — owned by MenuController. `<cr>` / `<esc>`
	// inside the MENU popup context.
	MenuConfirm = "menu.confirm"
	MenuCancel  = "menu.cancel"

	// PromptSubmit / PromptCancel / PromptBackspace — owned by
	// PromptController. `<cr>` / `<esc>` / `<bs>` inside the PROMPT
	// popup context. Printable-rune bindings register per-rune
	// closures under IDs of the form "prompt.rune.<hex>" — those IDs
	// are intentionally NOT listed in AllActionIDs (registry hygiene
	// only covers stable, user-visible IDs).
	PromptSubmit    = "prompt.submit"
	PromptCancel    = "prompt.cancel"
	PromptBackspace = "prompt.backspace"

	// SelectionUp / SelectionDown / SelectionConfirm / SelectionCancel —
	// owned by SelectionController. `<up>`/`k`, `<down>`/`j`, `<cr>`,
	// `<esc>` inside the SELECTION popup context (dbsavvy-m47.2).
	SelectionUp      = "selection.up"
	SelectionDown    = "selection.down"
	SelectionConfirm = "selection.confirm"
	SelectionCancel  = "selection.cancel"

	// CommandOpen — owned globally; opens the COMMAND_LINE context. `:`
	// CommandCancel — owned by COMMAND_LINE context; closes it. `<esc>`
	// CommandSubmit — owned by COMMAND_LINE context; submits the typed
	// buffer to the ExRegistry. `<cr>`
	// (All three are consumed by dlp.7's COMMAND_LINE bindings.)
	CommandOpen   = "command.open"
	CommandCancel = "command.cancel"
	CommandSubmit = "command.submit"

	// HelpCheatsheet — opens the auto-generated cheatsheet popup. `?`
	HelpCheatsheet = "help.cheatsheet"

	// Query family — owned by QueryEditorController (dbsavvy-66p.11).
	// Default bindings: <leader>r, <leader>R, <leader>e, <leader>E,
	// <leader>x, <leader>! respectively.
	QueryRun            = "query.run"
	QueryRunAll         = "query.run_all"
	QueryExplain        = "query.explain"
	QueryExplainAnalyze = "query.explain_analyze"
	QueryCancel         = "query.cancel"
	QueryRunInNewTx     = "query.run_in_new_tx"

	// Result-tab family — owned by ResultTabsController (dbsavvy-66p.12).
	// Jump bindings are GLOBAL-scoped so <leader>1..9 work from any
	// focused view; the close / pin / cancel / cycle bindings are
	// RESULT_GRID-scoped so they only fire when a result tab is active.
	ResultTabJump1  = "result.tab.jump.1"
	ResultTabJump2  = "result.tab.jump.2"
	ResultTabJump3  = "result.tab.jump.3"
	ResultTabJump4  = "result.tab.jump.4"
	ResultTabJump5  = "result.tab.jump.5"
	ResultTabJump6  = "result.tab.jump.6"
	ResultTabJump7  = "result.tab.jump.7"
	ResultTabJump8  = "result.tab.jump.8"
	ResultTabJump9  = "result.tab.jump.9"
	ResultTabNext   = "result.tab.next"
	ResultTabPrev   = "result.tab.prev"
	ResultTabClose  = "result.tab.close"
	ResultTabPin    = "result.tab.pin"
	ResultTabCancel = "result.tab.cancel"

	// Motion family — owned by VimEditorController (dbsavvy-wwd.5).
	// Defaults follow vim: w/b/e (word_*), W/B/E (WORD_*), 0/^/$,
	// gg/G, {/}/(/), h/j/k/l, H/M/L. mark_jump backs the `'a..z'
	// recall handler (wwd.3 mark recall surfaced to a binding by wwd.7).
	MotionWordNext          = "motion.word_next"
	MotionWordPrev          = "motion.word_prev"
	MotionWordNextBig       = "motion.word_next_big"
	MotionWordPrevBig       = "motion.word_prev_big"
	MotionWordEnd           = "motion.word_end"
	MotionWordEndBig        = "motion.word_end_big"
	MotionLineStart         = "motion.line_start"
	MotionLineFirstNonblank = "motion.line_first_nonblank"
	MotionLineEnd           = "motion.line_end"
	MotionBufferStart       = "motion.buffer_start"
	MotionBufferEnd         = "motion.buffer_end"
	MotionParagraphPrev     = "motion.paragraph_prev"
	MotionParagraphNext     = "motion.paragraph_next"
	MotionSentencePrev      = "motion.sentence_prev"
	MotionSentenceNext      = "motion.sentence_next"
	MotionLineDown          = "motion.line_down"
	MotionLineUp            = "motion.line_up"
	MotionCharLeft          = "motion.char_left"
	MotionCharRight         = "motion.char_right"
	MotionScreenTop         = "motion.screen_top"
	MotionScreenMiddle      = "motion.screen_middle"
	MotionScreenBottom      = "motion.screen_bottom"
	MotionMarkJump          = "motion.mark_jump"

	// Operator family — owned by VimEditorController (dbsavvy-wwd.8).
	// Defaults: d/y/c (delete/yank/change), gU/gu (upper/lower), >/< (indent
	// right/left). Each operator binds in Normal | OperatorPending | every
	// Visual variant. In Normal mode the operator stashes itself in
	// RepeatStore.PendingOpID and flips ModeStore[QUERY_EDITOR] to
	// ModeOperatorPending; the next motion/text-object completes via
	// VimEditorController.applyPending. In Visual mode the operator
	// consumes Buffer.Selection directly (bypasses op-pending per
	// Architecture Decision 4). In OperatorPending mode the same key as
	// the stashed operator triggers the linewise variant (dd/yy/cc/>>/<<).
	OperatorDelete      = "operator.delete"
	OperatorYank        = "operator.yank"
	OperatorChange      = "operator.change"
	OperatorUpper       = "operator.upper"
	OperatorLower       = "operator.lower"
	OperatorIndentRight = "operator.indent_right"
	OperatorIndentLeft  = "operator.indent_left"

	// EditorPaste — owned by VimEditorController (dbsavvy-wwd.8). Bound
	// to `p` in Normal mode. The handler reads from the effective
	// register (ec.Register, defaulting to '"') and inserts the text
	// after the cursor. LineWise registers (set by dd/yy) paste on a new
	// line below the cursor, matching vim semantics.
	EditorPaste = "editor.paste"

	// Text-object family — owned by VimEditorController (dbsavvy-wwd.6).
	// Defaults follow vim: i"/a" (double quote), i'/a' (single quote),
	// i(/a( (paren), i[/a[ (bracket), i{/a{ + iB/aB (brace),
	// ip/ap (paragraph — blank-line delimited per vim), is/as (SQL
	// statement — naive ';' split). Bindings live under OperatorPending
	// in wwd.6; the Visual / VisualLine mode mask is added in wwd.7.
	TextObjectInnerQuoteDouble = "textobject.inner_quote_double"
	TextObjectAroundQuoteDouble = "textobject.around_quote_double"
	TextObjectInnerQuoteSingle  = "textobject.inner_quote_single"
	TextObjectAroundQuoteSingle = "textobject.around_quote_single"
	TextObjectInnerParen        = "textobject.inner_paren"
	TextObjectAroundParen       = "textobject.around_paren"
	TextObjectInnerBracket      = "textobject.inner_bracket"
	TextObjectAroundBracket     = "textobject.around_bracket"
	TextObjectInnerBrace        = "textobject.inner_brace"
	TextObjectAroundBrace       = "textobject.around_brace"
	TextObjectInnerParagraph    = "textobject.inner_paragraph"
	TextObjectAroundParagraph   = "textobject.around_paragraph"
	TextObjectInnerStatement    = "textobject.inner_statement"
	TextObjectAroundStatement   = "textobject.around_statement"

	// Insert-entry family — owned by VimEditorController (dbsavvy-wwd.10).
	// Defaults: `i` enters Insert with cursor in place; `a` enters with
	// cursor moved one column right (append); `o`/`O` open a new line
	// below/above; `I`/`A` jump to first-non-blank / line-end+1 and
	// enter Insert. Each handler flips ModeStore[QUERY_EDITOR] to
	// ModeInsert after positioning the cursor (and applying the Insert
	// edit for o/O).
	InsertEnter         = "insert.enter"
	InsertAppend        = "insert.append"
	InsertOpenBelow     = "insert.open_below"
	InsertOpenAbove     = "insert.open_above"
	InsertFirstNonblank = "insert.first_nonblank"
	InsertAppendEnd     = "insert.append_end"

	// ModeNormal — owned by VimEditorController (dbsavvy-wwd.10).
	// Bound to `<esc>` in Insert mode; flips ModeStore[QUERY_EDITOR]
	// back to ModeNormal. Visual modes use VisualExit instead.
	ModeNormal = "mode.normal"

	// Editor history family — owned by VimEditorController (dbsavvy-wwd.10).
	// `u` invokes Buffer.Undo (replays the inverse of the most recent
	// Edit); `<c-r>` invokes Buffer.Redo (re-applies along children[0]
	// of the UndoTree). Both are no-ops at the tree boundaries.
	EditorUndo = "editor.undo"
	EditorRedo = "editor.redo"

	// EditorRepeat — owned by VimEditorController (dbsavvy-wwd.9). Bound
	// to `.` in Normal mode. The handler reads the most-recently-captured
	// operator from QueryEditorContext.RepeatStore, re-resolves the
	// motion or text-object range from the CURRENT cursor position
	// (vim semantics — `.` is not a pure replay of the original range),
	// and re-invokes the operator via the same applyPending pathway.
	EditorRepeat = "editor.repeat"

	// Visual / Selection family — owned by VimEditorController (dbsavvy-wwd.7).
	// Bindings: `v` / `V` / `<c-v>` enter char/line/block visual from Normal;
	// `<esc>` exits to Normal. SelectionExtend is the action ID covering
	// in-Visual motion dispatch (motion keys re-target ExtendSelection
	// instead of SetCursor); no default chord is published for it — it
	// piggybacks on the existing motion bindings under the Visual-mode
	// mask. The ID exists so the action registry can audit it.
	VisualEnter      = "visual.enter"
	VisualEnterLine  = "visual.enter_line"
	VisualEnterBlock = "visual.enter_block"
	VisualExit       = "visual.exit"
	SelectionExtend  = "selection.extend"
)

// AllActionIDs returns every ID declared in this file in declaration
// order. Useful for tests that want to assert every constant is
// non-empty, dot-namespaced, and unique without enumerating them by
// name. New constants MUST be appended here so the test catches the
// addition.
func AllActionIDs() []string {
	return []string{
		AppQuit,
		SchemaHide,
		SchemaUnhide,
		SchemaToggleShowHidden,
		ConnectionAdd,
		ListUp,
		ListDown,
		ListConfirm,
		RailSwitchSchemas,
		RailSwitchTables,
		RailSwitchColumns,
		RailSwitchIndexes,
		RailSwitchNext,
		MenuConfirm,
		MenuCancel,
		PromptSubmit,
		PromptCancel,
		PromptBackspace,
		SelectionUp,
		SelectionDown,
		SelectionConfirm,
		SelectionCancel,
		CommandOpen,
		CommandCancel,
		CommandSubmit,
		HelpCheatsheet,
		QueryRun,
		QueryRunAll,
		QueryExplain,
		QueryExplainAnalyze,
		QueryCancel,
		QueryRunInNewTx,
		ResultTabJump1,
		ResultTabJump2,
		ResultTabJump3,
		ResultTabJump4,
		ResultTabJump5,
		ResultTabJump6,
		ResultTabJump7,
		ResultTabJump8,
		ResultTabJump9,
		ResultTabNext,
		ResultTabPrev,
		ResultTabClose,
		ResultTabPin,
		ResultTabCancel,
		MotionWordNext,
		MotionWordPrev,
		MotionWordNextBig,
		MotionWordPrevBig,
		MotionWordEnd,
		MotionWordEndBig,
		MotionLineStart,
		MotionLineFirstNonblank,
		MotionLineEnd,
		MotionBufferStart,
		MotionBufferEnd,
		MotionParagraphPrev,
		MotionParagraphNext,
		MotionSentencePrev,
		MotionSentenceNext,
		MotionLineDown,
		MotionLineUp,
		MotionCharLeft,
		MotionCharRight,
		MotionScreenTop,
		MotionScreenMiddle,
		MotionScreenBottom,
		MotionMarkJump,
		OperatorDelete,
		OperatorYank,
		OperatorChange,
		OperatorUpper,
		OperatorLower,
		OperatorIndentRight,
		OperatorIndentLeft,
		EditorPaste,
		TextObjectInnerQuoteDouble,
		TextObjectAroundQuoteDouble,
		TextObjectInnerQuoteSingle,
		TextObjectAroundQuoteSingle,
		TextObjectInnerParen,
		TextObjectAroundParen,
		TextObjectInnerBracket,
		TextObjectAroundBracket,
		TextObjectInnerBrace,
		TextObjectAroundBrace,
		TextObjectInnerParagraph,
		TextObjectAroundParagraph,
		TextObjectInnerStatement,
		TextObjectAroundStatement,
		VisualEnter,
		VisualEnterLine,
		VisualEnterBlock,
		VisualExit,
		SelectionExtend,
		InsertEnter,
		InsertAppend,
		InsertOpenBelow,
		InsertOpenAbove,
		InsertFirstNonblank,
		InsertAppendEnd,
		ModeNormal,
		EditorUndo,
		EditorRedo,
		EditorRepeat,
	}
}
