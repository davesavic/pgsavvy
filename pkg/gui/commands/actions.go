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
	RailSwitchSchemas     = "rail.switch.schemas"
	RailSwitchTables      = "rail.switch.tables"
	RailSwitchQueryEditor = "rail.switch.query_editor"
	RailSwitchResults     = "rail.switch.results"
	RailSwitchNext        = "rail.switch.next"

	// Directional rail navigation (dbsavvy-xs0). Bound to Ctrl+K/J on the
	// five side rails (move focus up/down through the vertical stack;
	// no-op at the ends). RailSwitchLastRail is bound to Ctrl+H on the
	// QueryEditor — returns focus to the most-recently-focused rail so
	// Ctrl+L→editor / Ctrl+H→back-to-rail round-trips. Defaults to Schemas
	// before any rail has been focused.
	RailSwitchUp       = "rail.switch.up"
	RailSwitchDown     = "rail.switch.down"
	RailSwitchLastRail = "rail.switch.last_rail"

	// RailRefresh — published per-rail by the five side-rail controllers
	// (Connections / Schemas / Tables / Columns / Indexes). Bound to `r`
	// in Normal mode. Each rail's handler dispatches through
	// HelperBag.Refresh, which reloads the underlying data and pushes it
	// back into the rail context (dbsavvy-56u.1).
	RailRefresh = "rail.refresh"

	// MenuConfirm / MenuCancel — owned by MenuController. `<cr>` / `<esc>`
	// inside the MENU popup context.
	MenuConfirm = "menu.confirm"
	MenuCancel  = "menu.cancel"

	// PromptSubmit / PromptCancel — owned by PromptController.
	// `<cr>` / `<esc>` inside the PROMPT popup context. Printable
	// runes, Backspace, Delete, Left/Right/Home/End and bracketed
	// paste all flow through the master gocui.Editor's Passthrough
	// branch into gocui.DefaultEditor (dbsavvy-fq9 / dbsavvy-7k9 /
	// dbsavvy-f5t) — the PROMPT view is editable and gocui rejects
	// char-key SetKeybinding shims on editable views (matchView), so
	// per-rune action IDs no longer exist here.
	PromptSubmit = "prompt.submit"
	PromptCancel = "prompt.cancel"
	// PromptBackspace is retained as a dangling alias for source
	// compatibility with downstream pkgs that still reference the
	// constant; no controller registers a handler under this ID.
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

	// ConfirmYes / ConfirmNo — owned by ConfirmationController (dbsavvy-56u.2).
	// CONFIRMATION-scoped. y / <cr> invoke ConfirmHelper.Yes; n / <esc>
	// invoke ConfirmHelper.No. Defaults are hardcoded, not user-overridable
	// (AD-4).
	ConfirmYes = "confirm.yes"
	ConfirmNo  = "confirm.no"

	// ConnectingRetry / ConnectingCancel — owned by ConnectingController
	// (dbsavvy-e53.3). CONNECTING-scoped. r invokes the injected Retry
	// callback; <esc> invokes the injected Cancel callback. Defaults are
	// hardcoded, not user-overridable.
	ConnectingRetry  = "connecting.retry"
	ConnectingCancel = "connecting.cancel"

	// ConnectionManagerClose — owned by ConnectionManagerController
	// (dbsavvy-ig4). CONNECTION_MANAGER-scoped. <esc> pops the modal off
	// the focus stack, but is a no-op when the modal is the startup root
	// (never pops at stack bottom). q is bound at CONNECTION_MANAGER scope to
	// AppQuit; Ctrl-C quits via the GLOBAL-scope binding.
	ConnectionManagerClose = "connection_manager.close"

	// ConnectionManagerDown / Up / Confirm / Retry — owned by
	// ConnectionManagerController (dbsavvy-1rf). CONNECTION_MANAGER-scoped.
	// j/k move the list cursor; <CR> connects the selected profile (or
	// retries from the error state); r retries from the connecting/error
	// body. The connect lifecycle renders inside the modal (no standalone
	// CONNECTING push).
	ConnectionManagerDown    = "connection_manager.down"
	ConnectionManagerUp      = "connection_manager.up"
	ConnectionManagerConfirm = "connection_manager.confirm"
	ConnectionManagerRetry   = "connection_manager.retry"

	// TipDismiss — owned by the orchestrator's FirstRunTip wiring
	// (dbsavvy-56u.2). FIRST_RUN_TIP-scoped. Pops the tip popup and
	// stamps the seen-at timestamp via AppStateStore.StampStartupTips.
	TipDismiss = "tip.dismiss"

	// Query family — owned by QueryEditorController (dbsavvy-66p.11).
	// Default bindings: <leader>r, <leader>R, <leader>e, <leader>E,
	// <leader>x, <leader>! respectively.
	QueryRun            = "query.run"
	QueryRunAll         = "query.run_all"
	QueryExplain        = "query.explain"
	QueryExplainAnalyze = "query.explain_analyze"
	QueryCancel         = "query.cancel"
	QueryRunInNewTx     = "query.run_in_new_tx"
	QueryFormat         = "query.format"

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

	// Pagination + read-to-end (dbsavvy-uv0.3). RESULT_GRID-scoped:
	// ]p / [p / G fire only when a result tab is focused. ]G forces
	// ReadToEnd regardless of viewMode (dbsavvy-uv0.7 AD-14).
	ResultPageNext       = "result.page.next"
	ResultPagePrev       = "result.page.prev"
	ResultReadToEnd      = "result.read_to_end"
	ResultReadToEndForce = "result.read_to_end_force"

	// /regex filter family (dbsavvy-uv0.4). RESULT_GRID-scoped.
	//   ResultFilterPrompt    - /     opens the prompt; submit applies
	//   ResultFilterToggleAll - <C-a> toggles allCols on the active filter
	//   ResultFilterNext      - n     jump cursor to next match
	//   ResultFilterPrev      - N     jump cursor to previous match
	//   ResultFilterClear     - <esc> clear active filter
	ResultFilterPrompt    = "result.filter.prompt"
	ResultFilterToggleAll = "result.filter.toggle_all_cols"
	ResultFilterNext      = "result.filter.next"
	ResultFilterPrev      = "result.filter.prev"
	ResultFilterClear     = "result.filter.clear"

	// In-grid sort (dbsavvy-uv0.5). RESULT_GRID-scoped.
	//   ResultSortPick - <leader>s opens column picker; first invocation
	//                    on a column = asc, second = desc, third = clear.
	ResultSortPick = "result.sort.pick"

	// In-grid hide-columns overlay (dbsavvy-uv0.6). RESULT_GRID-scoped.
	//   ResultHideOverlay - <leader>gH opens the column-visibility overlay.
	//                       Persistence is gated on the active tab's
	//                       ResultIdentity.HasRowIdentity flag.
	ResultHideOverlay = "result.hide.overlay"

	// Expanded view mode (dbsavvy-uv0.7). RESULT_GRID-scoped.
	//   ResultViewToggle - <leader>gx flips the active grid between
	//                      grid and expanded view; persisted globally
	//                      via AppState.LastResultViewMode.
	//   Result grid motion chords (dispatched on viewMode by the helper).
	ResultViewToggle      = "result.view.toggle"
	ResultCursorDown      = "result.cursor.down"
	ResultCursorUp        = "result.cursor.up"
	ResultCursorLeft      = "result.cursor.left"
	ResultCursorRight     = "result.cursor.right"
	ResultColFirst        = "result.col.first"
	ResultColLast         = "result.col.last"
	ResultJumpFirst       = "result.jump.first"
	ResultJumpLast        = "result.jump.last"
	ResultHalfPageDown    = "result.half_page.down"
	ResultHalfPageUp      = "result.half_page.up"
	ResultWrappedLineDown = "result.wrapped_line.down"
	ResultWrappedLineUp   = "result.wrapped_line.up"
	ResultSelectRow       = "result.select.row"
	ResultSelectBlock     = "result.select.block"

	// Clipboard yank (dbsavvy U4). RESULT_GRID-scoped.
	//   ResultYankCell - `y` copies the focused cell's display value.
	//   ResultYankRow  - `yy` copies the focused row as TSV.
	ResultYankCell = "result.yank.cell"
	ResultYankRow  = "result.yank.row"

	// HideOverlay-scope handlers (dbsavvy-uv0.6) — owned by
	// HideOverlayController. j/k cursor moves, <space> toggle, <esc> /
	// q apply-and-close.
	HideOverlayUp     = "hide_overlay.up"
	HideOverlayDown   = "hide_overlay.down"
	HideOverlayToggle = "hide_overlay.toggle"
	HideOverlayClose  = "hide_overlay.close"

	// Result-export menu (dbsavvy-uv0.9). RESULT_GRID-scoped.
	//   ResultExportPrompt - <leader>oe opens the export-destination menu.
	//                        Chord registration is deferred until the menu
	//                        UI lands; this constant exists so the action
	//                        registry + i18n table can reserve the ID.
	ResultExportPrompt = "result.export.prompt"

	// ExportMenuUp / Down move the field cursor; Left / Right adjust the
	// value of the current field; Confirm executes; Cancel pops the popup;
	// ConfirmFullScopeWithFilter is the typed-YES handler for the
	// filter-conflict warning. dbsavvy-uv0.9.
	ExportMenuUp                         = "export.menu.up"
	ExportMenuDown                       = "export.menu.down"
	ExportMenuLeft                       = "export.menu.left"
	ExportMenuRight                      = "export.menu.right"
	ExportMenuConfirm                    = "export.menu.confirm"
	ExportMenuCancel                     = "export.menu.cancel"
	ExportMenuConfirmFullScopeWithFilter = "export.menu.confirm.full.filter"

	// TableInspectOpen opens the TABLE_INSPECT popup from the TABLES rail
	// (`i` in Normal mode). TableInspectNextTab / PrevTab cycle the tabs of
	// the TABLE_INSPECT popup (dbsavvy-3vf). Close pops the popup back to
	// the Tables rail.
	TableInspectOpen    = "table_inspect.open"
	TableInspectNextTab = "table_inspect.next_tab"
	TableInspectPrevTab = "table_inspect.prev_tab"
	TableInspectClose   = "table_inspect.close"

	// Plan family — owned by PlanController (dbsavvy-uv0.8). All PLAN-
	// scoped; published only when a plan tab is the focused context.
	//   PlanToggle        - <CR> toggles collapse on the cursor node
	//   PlanExpandAll     - <C-a> empties the collapsed map
	//   PlanCollapseAll   - <C-x> collapses every node except the root
	//   PlanJumpHeaviest  - H jumps cursor to heaviest descendant subtree
	//   PlanToggleRaw     - o flips raw-text vs tree view
	//   PlanCursorDown    - j cursor +1
	//   PlanCursorUp      - k cursor -1
	PlanToggle       = "plan.toggle"
	PlanExpandAll    = "plan.expand_all"
	PlanCollapseAll  = "plan.collapse_all"
	PlanJumpHeaviest = "plan.jump_heaviest"
	PlanToggleRaw    = "plan.toggle_raw"
	PlanCursorDown   = "plan.cursor_down"
	PlanCursorUp     = "plan.cursor_up"

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
	TextObjectInnerQuoteDouble  = "textobject.inner_quote_double"
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

	// Cell-edit family — owned by CellEditorController (dbsavvy-bwq A1/A2/Z1).
	// `i` enters the CELL_EDITOR popup over the cursor cell; `<cr>`/`<esc>`
	// commit; `<c-c>` discards; SetNull / Expr* are the per-type entry
	// helpers (<c-n>/<c-t>/<c-d>/<c-e>).
	CellEditEnter           = "cell.edit.enter"
	CellEditCommit          = "cell.edit.commit"
	CellEditDiscard         = "cell.edit.discard"
	CellEditSetNull         = "cell.edit.set_null"
	CellEditExprNow         = "cell.edit.expr.now"
	CellEditExprCurrentDate = "cell.edit.expr.current_date"
	CellEditExprPrompt      = "cell.edit.expr.prompt"

	// Commit-dialog family — owned by CommitDialogController (dbsavvy-bwq A4/A5/Z1).
	// `:w`/`<leader>cw` open; `[a]` apply (gated); `[d]` dry-run; `[s]` SQL
	// preview toggle; `[Esc]`/`[c]` cancel; TypeChar/Backspace drive the
	// typed-name input.
	CommitDialogOpen      = "commit.dialog.open"
	CommitDialogApply     = "commit.dialog.apply"
	CommitDialogDryRun    = "commit.dialog.dryrun"
	CommitDialogShowSql   = "commit.dialog.show_sql"
	CommitDialogCancel    = "commit.dialog.cancel"
	CommitDialogTypeChar  = "commit.dialog.type_char"
	CommitDialogBackspace = "commit.dialog.backspace"

	// Conflict-dialog family — owned by ConflictDialogController (dbsavvy-bwq A6/Z1).
	// `[r]` refresh; `[o]` overwrite (omitted on confirm_writes); `[Esc]` cancel.
	ConflictDialogRefresh   = "conflict.dialog.refresh"
	ConflictDialogOverwrite = "conflict.dialog.overwrite"
	ConflictDialogCancel    = "conflict.dialog.cancel"

	// FK reverse-picker family — owned by FKReversePickerController
	// (dbsavvy-bwq B6/Z1). `gD` opens the picker; <tab>/]/[ cycle tabs;
	// <cr> selects; <esc>/q closes.
	FKReverseMenu    = "row.fk_reverse_menu"
	FKReverseNextTab = "fk_reverse_picker.next_tab"
	FKReversePrevTab = "fk_reverse_picker.prev_tab"
	FKReverseSelect  = "fk_reverse_picker.select"
	FKReverseClose   = "fk_reverse_picker.close"

	// Pending-edit discard / force-quit family — owned by PendingDiscardHelper
	// + result_tabs_controller (dbsavvy-bwq A8/Z1). `<leader>cu` discards at
	// cursor; `<leader>cU` discards all (with confirmation > threshold);
	// `:q!` force-quits regardless of staged edits.
	PendingDiscardAtCursor = "pending.discard.at_cursor"
	PendingDiscardAll      = "pending.discard.all"
	QuitForce              = "app.quit.force"

	// FK forward-jump (dbsavvy-bwq B5/Z1). `gd` jumps to the referenced row.
	FKJumpForward = "row.fk_forward"

	// Result jump history (dbsavvy-bwq B5/B6/Z1). `<c-o>` back; `<c-i>` forward
	// through the per-grid jump list pushed by gd / gD.
	ResultJumpBack    = "result.jump.back"
	ResultJumpForward = "result.jump.forward"

	// Editor completion (dbsavvy-bwq/Z1). Manually triggers the completion
	// popup in QUERY_EDITOR insert mode (`<c-space>` default).
	EditorCompletionTrigger = "editor.completion.trigger"

	// Editor completion popup navigation (dbsavvy-etp.1). Insert-mode
	// bindings that drive the popup while it is visible; each handler
	// no-ops (leaving the key's normal Insert meaning) when hidden.
	EditorCompletionNext    = "editor.completion.next"
	EditorCompletionPrev    = "editor.completion.prev"
	EditorCompletionAccept  = "editor.completion.accept"
	EditorCompletionDismiss = "editor.completion.dismiss"

	// Reconnect — owned by ReconnectController (hq5.7). <leader>R in
	// GLOBAL scope triggers a Ping → reconnect dialog when the session
	// is disconnected. (QUERY_EDITOR uses <leader>R for QueryRunAll, so
	// this binding is GLOBAL-only and masked by the editor scope.)
	Reconnect = "app.reconnect"

	// SearchPathQuickSet — owned by SearchPathController (hq5.10).
	// <leader>p in GLOBAL scope opens a prompt pre-filled with
	// "SET search_path TO "; on submit the full text is fed to the
	// existing SET handler from hq5.8.
	SearchPathQuickSet = "session.search_path"

	// StatementTimeoutSet — owned by StatementTimeoutController (hq5.11).
	// <leader>tt in QUERY_EDITOR scope prompts for a postgres-style duration,
	// validates via session.CanonicalizeStatementTimeout, executes
	// SET statement_timeout on the session, and persists to AppState.
	StatementTimeoutSet = "session.statement_timeout"

	// Transaction family — owned by TxController (hq5.3). Default bindings:
	// <leader>tb, <leader>tc, <leader>tr, <leader>ts, <leader>tR, <leader>to.
	TxBegin               = "tx.begin"
	TxCommit              = "tx.commit"
	TxRollback            = "tx.rollback"
	TxSavepoint           = "tx.savepoint"
	TxReleaseSavepoint    = "tx.release_savepoint"
	TxRollbackToSavepoint = "tx.rollback_to_savepoint"

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
		RailSwitchQueryEditor,
		RailSwitchResults,
		RailSwitchNext,
		RailSwitchUp,
		RailSwitchDown,
		RailSwitchLastRail,
		RailRefresh,
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
		QueryFormat,
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
		ResultPageNext,
		ResultPagePrev,
		ResultReadToEnd,
		ResultReadToEndForce,
		ResultFilterPrompt,
		ResultFilterToggleAll,
		ResultFilterNext,
		ResultFilterPrev,
		ResultFilterClear,
		ResultSortPick,
		ResultHideOverlay,
		ResultViewToggle,
		ResultCursorDown,
		ResultCursorUp,
		ResultCursorLeft,
		ResultCursorRight,
		ResultColFirst,
		ResultColLast,
		ResultJumpFirst,
		ResultJumpLast,
		ResultHalfPageDown,
		ResultHalfPageUp,
		ResultWrappedLineDown,
		ResultWrappedLineUp,
		ResultSelectRow,
		ResultSelectBlock,
		ResultYankCell,
		ResultYankRow,
		HideOverlayUp,
		HideOverlayDown,
		HideOverlayToggle,
		HideOverlayClose,
		ResultExportPrompt,
		ExportMenuUp,
		ExportMenuDown,
		ExportMenuLeft,
		ExportMenuRight,
		ExportMenuConfirm,
		ExportMenuCancel,
		ExportMenuConfirmFullScopeWithFilter,
		TableInspectOpen,
		TableInspectNextTab,
		TableInspectPrevTab,
		TableInspectClose,
		PlanToggle,
		PlanExpandAll,
		PlanCollapseAll,
		PlanJumpHeaviest,
		PlanToggleRaw,
		PlanCursorDown,
		PlanCursorUp,
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
		ConfirmYes,
		ConfirmNo,
		ConnectingRetry,
		ConnectingCancel,
		ConnectionManagerClose,
		ConnectionManagerDown,
		ConnectionManagerUp,
		ConnectionManagerConfirm,
		ConnectionManagerRetry,
		TipDismiss,
		CellEditEnter,
		CellEditCommit,
		CellEditDiscard,
		CellEditSetNull,
		CellEditExprNow,
		CellEditExprCurrentDate,
		CellEditExprPrompt,
		CommitDialogOpen,
		CommitDialogApply,
		CommitDialogDryRun,
		CommitDialogShowSql,
		CommitDialogCancel,
		CommitDialogTypeChar,
		CommitDialogBackspace,
		ConflictDialogRefresh,
		ConflictDialogOverwrite,
		ConflictDialogCancel,
		FKReverseMenu,
		FKReverseNextTab,
		FKReversePrevTab,
		FKReverseSelect,
		FKReverseClose,
		PendingDiscardAtCursor,
		PendingDiscardAll,
		QuitForce,
		FKJumpForward,
		ResultJumpBack,
		ResultJumpForward,
		EditorCompletionTrigger,
		EditorCompletionNext,
		EditorCompletionPrev,
		EditorCompletionAccept,
		EditorCompletionDismiss,
		Reconnect,
		SearchPathQuickSet,
		StatementTimeoutSet,
		TxBegin,
		TxCommit,
		TxRollback,
		TxSavepoint,
		TxReleaseSavepoint,
		TxRollbackToSavepoint,
	}
}
