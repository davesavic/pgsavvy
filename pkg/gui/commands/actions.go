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
//   - Existing E1–E4 controller publishings (earlier KeyBinding lists)
//
// Out of scope here: query/result/cursor/pane families (added by
// later epics). The `:reload` command is registered
// via the ExRegistry, not the CommandRegistry — so no `reload.config`
// constant appears here.
//
// Namespace ownership:
//
//   - The `motion.*`, `operator.*`, and `textobject.*` namespaces are
//     owned exclusively by VimEditorController. No
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

	// ListUp / ListDown / ListConfirm — published by ListControllerTrait
	// (shared by every side-rail). `j`/`k`/`<cr>`.
	ListUp      = "list.up"
	ListDown    = "list.down"
	ListConfirm = "list.confirm"

	// ListJumpFirst / ListJumpLast — published by ListControllerTrait
	// (shared by every side-rail). `gg`/`G`.
	ListJumpFirst = "list.jump.first"
	ListJumpLast  = "list.jump.last"

	// RailPanLeft / RailPanRight / RailPanStart / RailPanEnd — published by
	// ListControllerTrait (shared by every side-rail). `h`/`l`/`0`/`$` scroll
	// the rail horizontally so names wider than the pane can be read in full.
	RailPanLeft  = "rail.pan.left"
	RailPanRight = "rail.pan.right"
	RailPanStart = "rail.pan.start"
	RailPanEnd   = "rail.pan.end"

	// Rail-switch family — published by the SchemaRailController and the
	// QUERY_EDITOR / RESULT_GRID / PLAN controllers via railSwitchBindings
	// (pkg/gui/controllers/shared.go). Digits 3/4 jump to the QueryEditor /
	// results pane; `<tab>` cycles to the next pane.
	RailSwitchQueryEditor = "rail.switch.query_editor"
	RailSwitchResults     = "rail.switch.results"
	RailSwitchNext        = "rail.switch.next"

	// RailSwitchLastRail returns focus to the SCHEMA_RAIL container. Bound
	// to Ctrl+H on the QueryEditor / RESULT_GRID / PLAN so
	// Ctrl+L→editor / Ctrl+H→back-to-rail round-trips. The consolidated rail
	// is the single side-context, so there is no per-rail vertical stack any
	// more (the old Ctrl+K/J up/down navigation was dropped with '1'/'2').
	RailSwitchLastRail = "rail.switch.last_rail"

	// RailTabNext / RailTabPrev cycle the SCHEMA_RAIL container's active tab
	// (Schemas ⇄ Tables) with edge-wrap. Bound to `]` / `[` under
	// SCHEMA_RAIL. Owned by SchemaRailController.
	RailTabNext = "rail.tab.next"
	RailTabPrev = "rail.tab.prev"

	// QueryRailTabNext / QueryRailTabPrev cycle the QUERY_RAIL container's
	// active tab (QueryEditor ⇄ SavedQuery ⇄ History) with edge-wrap. Bound to
	// `]` / `[` under each leaf's OWN scope (QUERY_EDITOR / SAVED_QUERY /
	// HISTORY), Normal mode only. Handlers registered at the orchestrator level
	// (RegisterQueryRailTabActions) since the container is not owned by a
	// single controller.
	QueryRailTabNext = "queryrail.tab.next"
	QueryRailTabPrev = "queryrail.tab.prev"

	// SchemaRail family — owned by SchemaRailController. The consolidated
	// SCHEMA_RAIL container publishes every rail chord exactly once and
	// dispatches to the active leaf (Schemas / Tables): the nav chords drive
	// whichever leaf's cursor is active; SchemaRailConfirm / SchemaRailRefresh
	// dispatch by ActiveTab; the tab-unique chords (inspect / hide / unhide /
	// toggle-show-hidden) no-op when the owning tab is not active.
	SchemaRailUp               = "schema_rail.up"
	SchemaRailDown             = "schema_rail.down"
	SchemaRailJumpFirst        = "schema_rail.jump.first"
	SchemaRailJumpLast         = "schema_rail.jump.last"
	SchemaRailPanLeft          = "schema_rail.pan.left"
	SchemaRailPanRight         = "schema_rail.pan.right"
	SchemaRailPanStart         = "schema_rail.pan.start"
	SchemaRailPanEnd           = "schema_rail.pan.end"
	SchemaRailConfirm          = "schema_rail.confirm"
	SchemaRailRefresh          = "schema_rail.refresh"
	SchemaRailInspect          = "schema_rail.inspect"
	SchemaRailHide             = "schema_rail.hide"
	SchemaRailUnhide           = "schema_rail.unhide"
	SchemaRailToggleShowHidden = "schema_rail.toggle_show_hidden"

	// RailRefresh — published per-rail by the five side-rail controllers
	// (Connections / Schemas / Tables / Columns / Indexes). Bound to `r`
	// in Normal mode. Each rail's handler dispatches through
	// HelperBag.Refresh, which reloads the underlying data and pushes it
	// back into the rail context.
	RailRefresh = "rail.refresh"

	// MenuConfirm / MenuCancel — owned by MenuController. `<cr>` / `<esc>`
	// inside the MENU popup context.
	MenuConfirm = "menu.confirm"
	MenuCancel  = "menu.cancel"

	// PromptSubmit / PromptCancel — owned by PromptController.
	// `<cr>` / `<esc>` inside the PROMPT popup context. Printable
	// runes, Backspace, Delete, Left/Right/Home/End and bracketed
	// paste all flow through the master gocui.Editor's Passthrough
	// branch into gocui.DefaultEditor — the PROMPT view is editable and gocui rejects
	// char-key SetKeybinding shims on editable views (matchView), so
	// per-rune action IDs no longer exist here.
	PromptSubmit = "prompt.submit"
	PromptCancel = "prompt.cancel"

	// SelectionUp / SelectionDown / SelectionConfirm / SelectionCancel —
	// owned by SelectionController. `<up>`/`k`, `<down>`/`j`, `<cr>`,
	// `<esc>` inside the SELECTION popup context.
	SelectionUp      = "selection.up"
	SelectionDown    = "selection.down"
	SelectionConfirm = "selection.confirm"
	SelectionCancel  = "selection.cancel"

	// CommandOpen — owned globally; opens the COMMAND_LINE context. `:`
	// CommandCancel — owned by COMMAND_LINE context; closes it. `<esc>`
	// CommandSubmit — owned by COMMAND_LINE context; submits the typed
	// buffer to the ExRegistry. `<cr>`
	// (All three are consumed by the COMMAND_LINE bindings.)
	CommandOpen   = "command.open"
	CommandCancel = "command.cancel"
	CommandSubmit = "command.submit"

	// HelpCheatsheet — opens the auto-generated cheatsheet popup. `?`
	HelpCheatsheet = "help.cheatsheet"

	// ConfirmYes / ConfirmNo — owned by ConfirmationController.
	// CONFIRMATION-scoped. y / <cr> invoke ConfirmHelper.Yes; n / <esc>
	// invoke ConfirmHelper.No. Defaults are hardcoded, not user-overridable
	// (AD-4).
	ConfirmYes = "confirm.yes"
	ConfirmNo  = "confirm.no"

	// ConnectionManagerQuitOrClose — owned by ConnectionManagerController.
	// q on the modal: quits when the modal is the startup root
	// (stack depth == 1), closes the modal back to data when opened mid-session
	// (stack depth > 1).
	ConnectionManagerQuitOrClose = "connection_manager.quit_or_close"

	// ConnectionManagerOpen — GLOBAL-scoped action that opens the
	// CONNECTION_MANAGER modal mid-session (<leader>C).
	ConnectionManagerOpen = "connection_manager.open"

	// ConnectionManagerClose — owned by ConnectionManagerController.
	// CONNECTION_MANAGER-scoped. <esc> pops the modal off
	// the focus stack, but is a no-op when the modal is the startup root
	// (never pops at stack bottom). q is bound at CONNECTION_MANAGER scope to
	// AppQuit; Ctrl-C quits via the GLOBAL-scope binding.
	ConnectionManagerClose = "connection_manager.close"

	// ConnectionManagerDown / Up / Confirm / Retry — owned by
	// ConnectionManagerController. CONNECTION_MANAGER-scoped.
	// j/k move the list cursor; <CR> connects the selected profile (or
	// retries from the error state); r retries from the connecting/error
	// body. The connect lifecycle renders inside the modal (no standalone
	// CONNECTING push).
	ConnectionManagerDown      = "connection_manager.down"
	ConnectionManagerUp        = "connection_manager.up"
	ConnectionManagerConfirm   = "connection_manager.confirm"
	ConnectionManagerRetry     = "connection_manager.retry"
	ConnectionManagerJumpFirst = "connection_manager.jump.first"
	ConnectionManagerJumpLast  = "connection_manager.jump.last"

	// ConnectionManager add/edit form actions.
	// CONNECTION_MANAGER-scoped. In ModeList a opens a blank add form and e
	// edits the selected row. In ModeForm: Tab / Shift-Tab move field focus,
	// i edits the focused field (text → PROMPT popup; toggle/driver flip),
	// space toggles/cycles the focused field. Enter (Confirm) saves and Esc
	// (Close) cancels back to the list — both mode-gated on the existing
	// actions.
	ConnectionManagerAdd       = "connection_manager.add"
	ConnectionManagerEdit      = "connection_manager.edit"
	ConnectionManagerFieldNext = "connection_manager.field_next"
	ConnectionManagerFieldPrev = "connection_manager.field_prev"
	ConnectionManagerFieldEdit = "connection_manager.field_edit"
	ConnectionManagerToggle    = "connection_manager.toggle"
	ConnectionManagerDelete    = "connection_manager.delete"
	ConnectionManagerPasteDSN  = "connection_manager.paste_dsn"

	// ConnectionManagerTestConnection — owned by ConnectionManagerController.
	// CONNECTION_MANAGER-scoped (form mode). `t` dials the IN-PROGRESS
	// (unsaved) connection being edited and reports pass/fail INLINE in the
	// form WITHOUT establishing the real session or disturbing the live active
	// connection. Saving is independent of the test result.
	ConnectionManagerTestConnection = "connection_manager.test_connection"

	// RelationshipPanel family — owned by RelationshipPanelController.
	//   RelationshipPanelToggle - <leader>gr opens / closes the right-docked
	//                             FK sidebar (RESULT_GRID scope).
	//   RelationshipPanelEnter  - <cr> gives the panel input focus
	//                             (RELATIONSHIP_PANEL scope; T1 stub).
	//   RelationshipPanelExit   - <esc> returns focus to the grid with the
	//                             panel still open (RELATIONSHIP_PANEL scope).
	//   RelationshipPanelDown/Up - j/k move the in-panel selection cursor over
	//                             the relationship lines (RELATIONSHIP_PANEL
	//                             scope; only reachable while focused).
	RelationshipPanelToggle = "relationship_panel.toggle"
	RelationshipPanelEnter  = "relationship_panel.enter"
	RelationshipPanelExit   = "relationship_panel.exit"
	RelationshipPanelDown   = "relationship_panel.down"
	RelationshipPanelUp     = "relationship_panel.up"

	// TipDismiss — owned by the orchestrator's FirstRunTip wiring.
	// FIRST_RUN_TIP-scoped. Pops the tip popup and
	// stamps the seen-at timestamp via AppStateStore.StampStartupTips.
	TipDismiss = "tip.dismiss"

	// ChangelogDismiss — owned by the orchestrator's Changelog wiring.
	// CHANGELOG-scoped. Pops the changelog popup and
	// stamps AppState.Version via AppStateStore.StampVersion.
	ChangelogDismiss = "changelog.dismiss"

	// Query family — owned by QueryEditorController.
	// Default bindings: <leader>r, <leader>R, <leader>e, <leader>E,
	// <leader>x, <leader>! respectively.
	QueryRun            = "query.run"
	QueryRunAll         = "query.run_all"
	QueryExplain        = "query.explain"
	QueryExplainAnalyze = "query.explain_analyze"
	QueryCancel         = "query.cancel"
	QueryRunInNewTx     = "query.run_in_new_tx"
	QueryFormat         = "query.format"

	// Result-tab family — owned by ResultTabsController.
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

	// Pagination + read-to-end. RESULT_GRID-scoped:
	// ]p / [p / G fire only when a result tab is focused. ]G forces
	// ReadToEnd regardless of viewMode (AD-14).
	ResultPageNext       = "result.page.next"
	ResultPagePrev       = "result.page.prev"
	ResultReadToEnd      = "result.read_to_end"
	ResultReadToEndForce = "result.read_to_end_force"

	// In-grid search family. RESULT_GRID-scoped. The ID
	// STRING values keep their historical "result.filter.*" form so user
	// keybindings.yaml entries that bind by string stay stable across the
	// filter→search rename.
	//   ResultFilterPrompt - /     opens the live search input
	//   ResultFilterNext   - n     move cursor to next match
	//   ResultFilterPrev   - N     move cursor to previous match
	//   ResultFilterClear  - <esc> clear active search
	ResultFilterPrompt = "result.filter.prompt"
	ResultFilterNext   = "result.filter.next"
	ResultFilterPrev   = "result.filter.prev"
	ResultFilterClear  = "result.filter.clear"

	// Left-rail (Schemas/Tables) highlight+jump search.
	//   RailSearchPrompt - /     opens the search input on the focused rail
	//   RailSearchNext   - n     jump to next rail match
	//   RailSearchPrev   - N     jump to previous rail match
	//   RailSearchClear  - <esc> clear active rail search (no-op when inactive)
	RailSearchPrompt = "rail.search.prompt"
	RailSearchNext   = "rail.search.next"
	RailSearchPrev   = "rail.search.prev"
	RailSearchClear  = "rail.search.clear"

	// SEARCH_LINE-scoped accept / cancel. Internal IDs —
	// not bound by user keybindings.yaml; driven by the SearchLine
	// controller's <cr> / <esc> bindings.
	ResultSearchAccept = "result.search.accept"
	ResultSearchCancel = "result.search.cancel"

	// In-grid sort. RESULT_GRID-scoped.
	//   ResultSortPick - <leader>s opens column picker; first invocation
	//                    on a column = asc, second = desc, third = clear.
	ResultSortPick = "result.sort.pick"

	// In-grid hide-columns overlay. RESULT_GRID-scoped.
	//   ResultHideOverlay - <leader>gH opens the column-visibility overlay.
	//                       Persistence is gated on the active tab's
	//                       ResultIdentity.HasRowIdentity flag.
	ResultHideOverlay = "result.hide.overlay"

	// Result view cell-content viewer. RESULT_GRID-scoped.
	//   ResultViewCellOpen - <leader>gv opens the full cell-content viewer
	//                        popup for the focused cell.
	ResultViewCellOpen = "result.view.cell_open"

	// Expanded view mode. RESULT_GRID-scoped.
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
	ResultSelectCell      = "result.select.cell"

	// Clipboard yank. RESULT_GRID-scoped.
	//   ResultYankCell - `y` copies the focused cell's display value.
	//   ResultYankRow  - `yy` copies the focused row as TSV.
	ResultYankCell = "result.yank.cell"
	ResultYankRow  = "result.yank.row"

	// HideOverlay-scope handlers — owned by
	// HideOverlayController. j/k cursor moves, <space> toggle, <esc> /
	// q apply-and-close.
	HideOverlayUp     = "hide_overlay.up"
	HideOverlayDown   = "hide_overlay.down"
	HideOverlayToggle = "hide_overlay.toggle"
	HideOverlayClose  = "hide_overlay.close"

	// Result-export menu. RESULT_GRID-scoped.
	//   ResultExportPrompt - <leader>oe opens the export-destination menu.
	//                        Chord registration is deferred until the menu
	//                        UI lands; this constant exists so the action
	//                        registry + i18n table can reserve the ID.
	ResultExportPrompt = "result.export.prompt"

	// ExportMenuUp / Down move the field cursor; Left / Right adjust the
	// value of the current field; Confirm executes; Cancel pops the popup.
	ExportMenuUp      = "export.menu.up"
	ExportMenuDown    = "export.menu.down"
	ExportMenuLeft    = "export.menu.left"
	ExportMenuRight   = "export.menu.right"
	ExportMenuConfirm = "export.menu.confirm"
	ExportMenuCancel  = "export.menu.cancel"
	// ExportMenuEditPath opens the editable PROMPT seeded with the current
	// File-destination Path. No-op unless the Path field is active.
	ExportMenuEditPath = "export.menu.editpath"

	// TableInspectOpen opens the TABLE_INSPECT popup from the TABLES rail
	// (`i` in Normal mode). TableInspectNextTab / PrevTab cycle the tabs of
	// the TABLE_INSPECT popup. Close pops the popup back to
	// the Tables rail.
	TableInspectOpen    = "table_inspect.open"
	TableInspectNextTab = "table_inspect.next_tab"
	TableInspectPrevTab = "table_inspect.prev_tab"
	TableInspectClose   = "table_inspect.close"

	// HistoryOpen switches the QUERY_RAIL container to the History tab from
	// the QUERY_EDITOR (`<leader>h` in Normal mode). The History leaf loads
	// Recent(N) lazily on its first activation (and after a query run).
	HistoryOpen = "history.open"

	// QuerySavedOpen switches the QUERY_RAIL container to the Saved Queries
	// tab from the QUERY_EDITOR (`<leader>o` in Normal mode). The Saved Queries
	// leaf loads queries.yml lazily on its first activation (and after a
	// save/delete).
	QuerySavedOpen = "query.saved.open"

	// QuerySavedDelete deletes the saved query under the cursor (dd) after a
	// confirmation, then re-reads queries.yml.
	QuerySavedDelete = "query.saved.delete"

	// QuerySave captures the current query text (visual selection or the
	// statement under cursor) from the QUERY_EDITOR (`<leader>s` in Normal
	// mode), prompts for a name, and persists it to queries.yml —
	// overwrite-confirming on a name collision.
	QuerySave = "query.save"

	// QueryOpenFile opens the file picker to load an SQL file into the editor.
	QueryOpenFile = "query.open_file"

	// Plan family — owned by PlanController. All PLAN-
	// scoped; published only when a plan tab is the focused context.
	//   PlanToggle        - <CR> toggles collapse on the cursor node
	//   PlanExpandAll     - <C-a> empties the collapsed map
	//   PlanCollapseAll   - <C-x> collapses every node except the root
	//   PlanJumpHeaviest  - H jumps cursor to heaviest descendant subtree
	//   PlanToggleRaw     - o flips raw-text vs tree view
	//   PlanToggleInsights- i shows/hides the plan-doctor insights strip
	//   PlanCursorDown    - j cursor +1 (or strip selection +1 when insights active)
	//   PlanCursorUp      - k cursor -1 (or strip selection -1 when insights active)
	PlanToggle         = "plan.toggle"
	PlanExpandAll      = "plan.expand_all"
	PlanCollapseAll    = "plan.collapse_all"
	PlanJumpHeaviest   = "plan.jump_heaviest"
	PlanToggleRaw      = "plan.toggle_raw"
	PlanToggleInsights = "plan.toggle_insights"
	PlanCursorDown     = "plan.cursor_down"
	PlanCursorUp       = "plan.cursor_up"

	// Motion family — owned by VimEditorController.
	// Defaults follow vim: w/b/e (word_*), W/B/E (WORD_*), 0/^/$,
	// gg/G, {/}/(/), h/j/k/l, H/M/L.
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

	// Operator family — owned by VimEditorController.
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
	// OperatorDeleteEndOfLine — vim `D` (single-key alias for `d$`):
	// deletes from the cursor to the end of the current line, char-wise,
	// writing the span to the register. Normal-mode only.
	OperatorDeleteEndOfLine = "operator.delete_eol"

	// EditorPaste — owned by VimEditorController. Bound
	// to `p` in Normal mode. The handler reads from the effective
	// register (ec.Register, defaulting to '"') and inserts the text
	// after the cursor. LineWise registers (set by dd/yy) paste on a new
	// line below the cursor, matching vim semantics.
	EditorPaste = "editor.paste"

	// EditorPasteBefore — owned by VimEditorController. Bound to `P` in
	// Normal + visual modes. Mirrors EditorPaste but inserts before the
	// cursor (char-wise) / above the line (line-wise), matching vim `P`.
	EditorPasteBefore = "editor.paste_before"

	// EditorToggleCase — owned by VimEditorController. Bound to `~` in
	// Normal + visual modes. Toggles the case of the char under the
	// cursor (count chars) and advances, or the visual selection. With
	// tildeop off (default) `~` is NOT an operator — it acts immediately.
	EditorToggleCase = "editor.toggle_case"

	// Text-object family — owned by VimEditorController.
	// Defaults follow vim: i"/a" (double quote), i'/a' (single quote),
	// i(/a( (paren), i[/a[ (bracket), i{/a{ + iB/aB (brace),
	// ip/ap (paragraph — blank-line delimited per vim), is/as (SQL
	// statement — naive ';' split). Bindings live under OperatorPending,
	// with the Visual / VisualLine mode mask added on top.
	TextObjectInnerWord         = "textobject.inner_word"
	TextObjectAroundWord        = "textobject.around_word"
	TextObjectInnerWORD         = "textobject.inner_word_big"
	TextObjectAroundWORD        = "textobject.around_word_big"
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

	// Insert-entry family — owned by VimEditorController.
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

	// ModeNormal — owned by VimEditorController.
	// Bound to `<esc>` in Insert mode; flips ModeStore[QUERY_EDITOR]
	// back to ModeNormal. Visual modes use VisualExit instead.
	ModeNormal = "mode.normal"

	// Editor history family — owned by VimEditorController.
	// `u` invokes Buffer.Undo (replays the inverse of the most recent
	// Edit); `<c-r>` invokes Buffer.Redo (re-applies along children[0]
	// of the UndoTree). Both are no-ops at the tree boundaries.
	EditorUndo = "editor.undo"
	EditorRedo = "editor.redo"

	// EditorRepeat — owned by VimEditorController. Bound
	// to `.` in Normal mode. The handler reads the most-recently-captured
	// operator from QueryEditorContext.RepeatStore, re-resolves the
	// motion or text-object range from the CURRENT cursor position
	// (vim semantics — `.` is not a pure replay of the original range),
	// and re-invokes the operator via the same applyPending pathway.
	EditorRepeat = "editor.repeat"

	// Cell-edit family — owned by CellEditorController.
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

	// Commit-dialog family — owned by CommitDialogController.
	// `:w`/`<leader>cw` open; `[a]` apply (gated); `[d]` dry-run; `[s]` SQL
	// preview toggle; `[Esc]`/`[c]` cancel; `[t]` opens the typed-name
	// prompt that drives the confirm_writes apply gate.
	CommitDialogOpen     = "commit.dialog.open"
	CommitDialogApply    = "commit.dialog.apply"
	CommitDialogDryRun   = "commit.dialog.dryrun"
	CommitDialogShowSql  = "commit.dialog.show_sql"
	CommitDialogCancel   = "commit.dialog.cancel"
	CommitDialogTypeName = "commit.dialog.type_name"

	// Conflict-dialog family — owned by ConflictDialogController.
	// `[r]` refresh; `[o]` overwrite (omitted on confirm_writes); `[Esc]` cancel.
	ConflictDialogRefresh   = "conflict.dialog.refresh"
	ConflictDialogOverwrite = "conflict.dialog.overwrite"
	ConflictDialogCancel    = "conflict.dialog.cancel"

	// FK reverse-picker family — owned by FKReversePickerController.
	// `gD` opens the picker; <tab>/]/[ cycle tabs;
	// <cr> selects; <esc>/q closes.
	FKReverseMenu    = "row.fk_reverse_menu"
	FKReverseNextTab = "fk_reverse_picker.next_tab"
	FKReversePrevTab = "fk_reverse_picker.prev_tab"
	FKReverseSelect  = "fk_reverse_picker.select"
	FKReverseClose   = "fk_reverse_picker.close"

	// Pending-edit discard family — owned by PendingDiscardHelper
	// + result_tabs_controller. `<leader>cu` discards at
	// cursor; `<leader>cU` discards all (with confirmation > threshold).
	PendingDiscardAtCursor = "pending.discard.at_cursor"
	PendingDiscardAll      = "pending.discard.all"

	// FK forward-jump. `gd` jumps to the referenced row.
	FKJumpForward = "row.fk_forward"

	// Result jump history. `<c-o>` back; `<c-i>` forward
	// through the per-grid jump list pushed by gd / gD.
	ResultJumpBack    = "result.jump.back"
	ResultJumpForward = "result.jump.forward"

	// Editor completion. Manually triggers the completion
	// popup in QUERY_EDITOR insert mode (`<c-space>` default).
	EditorCompletionTrigger = "editor.completion.trigger"

	// Editor completion popup navigation. Insert-mode
	// bindings that drive the popup while it is visible; each handler
	// no-ops (leaving the key's normal Insert meaning) when hidden.
	EditorCompletionNext    = "editor.completion.next"
	EditorCompletionPrev    = "editor.completion.prev"
	EditorCompletionAccept  = "editor.completion.accept"
	EditorCompletionDismiss = "editor.completion.dismiss"

	// Reconnect — owned by ReconnectController. <leader>R in
	// GLOBAL scope triggers a Ping → reconnect dialog when the session
	// is disconnected. (QUERY_EDITOR uses <leader>R for QueryRunAll, so
	// this binding is GLOBAL-only and masked by the editor scope.)
	Reconnect = "app.reconnect"

	// SearchPathQuickSet — owned by SearchPathController.
	// <leader>p in GLOBAL scope opens a prompt pre-filled with
	// "SET search_path TO "; on submit the full text is fed to the
	// existing SET handler.
	SearchPathQuickSet = "session.search_path"

	// StatementTimeoutSet — owned by StatementTimeoutController.
	// <leader>tt in QUERY_EDITOR scope prompts for a postgres-style duration,
	// validates via session.CanonicalizeStatementTimeout, executes
	// SET statement_timeout on the session, and persists to AppState.
	StatementTimeoutSet = "session.statement_timeout"

	// Transaction family — owned by TxController. Default bindings:
	// <leader>tb, <leader>tc, <leader>tr, <leader>ts, <leader>tR, <leader>to.
	TxBegin               = "tx.begin"
	TxCommit              = "tx.commit"
	TxRollback            = "tx.rollback"
	TxSavepoint           = "tx.savepoint"
	TxReleaseSavepoint    = "tx.release_savepoint"
	TxRollbackToSavepoint = "tx.rollback_to_savepoint"

	// SettingsOpen — GLOBAL-scoped action that opens the
	// SETTINGS modal (<leader>cS). The handler is registered by the
	// orchestrator; the controller owns the 10 in-modal bindings below.
	SettingsOpen = "settings.open"

	// Settings family — owned by SettingsController.
	// SETTINGS-scoped. [ / ] cycle tabs; j/k move field focus; i edits
	// the focused text field (PROMPT popup); space toggles the focused
	// toggle; Enter saves; Esc closes; a/d add/delete keybindings on
	// the Keys tab.
	SettingsClose            = "settings.close"
	SettingsNextTab          = "settings.next_tab"
	SettingsPrevTab          = "settings.prev_tab"
	SettingsFieldUp          = "settings.field_up"
	SettingsFieldDown        = "settings.field_down"
	SettingsFieldEdit        = "settings.field_edit"
	SettingsFieldToggle      = "settings.field_toggle"
	SettingsConfirm          = "settings.confirm"
	SettingsKeybindingAdd    = "settings.keybinding_add"
	SettingsKeybindingDelete = "settings.keybinding_delete"

	// Visual / Selection family — owned by VimEditorController.
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

	// File Picker family — owned by FilePickerController.
	// Bindings: j/k move cursor, gg/G jump, Enter confirm, h/Backspace ascend,
	// q/Esc cancel, / search, . toggle hidden, n create directory, Tab focus input.
	FilePickerUp         = "file_picker.up"
	FilePickerDown       = "file_picker.down"
	FilePickerJumpFirst  = "file_picker.jump_first"
	FilePickerJumpLast   = "file_picker.jump_last"
	FilePickerConfirm    = "file_picker.confirm"
	FilePickerAscend     = "file_picker.ascend"
	FilePickerCancel     = "file_picker.cancel"
	FilePickerSearch     = "file_picker.search"
	FilePickerSearchNext = "file_picker.search_next"
	FilePickerSearchPrev = "file_picker.search_prev"
	FilePickerHidden     = "file_picker.hidden"
	FilePickerNewDir     = "file_picker.new_dir"
	FilePickerSort       = "file_picker.sort"
	FilePickerHome       = "file_picker.home"
	FilePickerFocusInput = "file_picker.focus_input"
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
		ListUp,
		ListDown,
		ListConfirm,
		ListJumpFirst,
		ListJumpLast,
		RailPanLeft,
		RailPanRight,
		RailPanStart,
		RailPanEnd,
		RailSwitchQueryEditor,
		RailSwitchResults,
		RailSwitchNext,
		RailSwitchLastRail,
		RailTabNext,
		RailTabPrev,
		QueryRailTabNext,
		QueryRailTabPrev,
		SchemaRailUp,
		SchemaRailDown,
		SchemaRailJumpFirst,
		SchemaRailJumpLast,
		SchemaRailPanLeft,
		SchemaRailPanRight,
		SchemaRailPanStart,
		SchemaRailPanEnd,
		SchemaRailConfirm,
		SchemaRailRefresh,
		SchemaRailInspect,
		SchemaRailHide,
		SchemaRailUnhide,
		SchemaRailToggleShowHidden,
		RailRefresh,
		MenuConfirm,
		MenuCancel,
		PromptSubmit,
		PromptCancel,
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
		ResultFilterNext,
		ResultFilterPrev,
		ResultFilterClear,
		RailSearchPrompt,
		RailSearchNext,
		RailSearchPrev,
		RailSearchClear,
		ResultSearchAccept,
		ResultSearchCancel,
		ResultSortPick,
		ResultHideOverlay,
		ResultViewCellOpen,
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
		ResultSelectCell,
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
		ExportMenuEditPath,
		TableInspectOpen,
		TableInspectNextTab,
		TableInspectPrevTab,
		TableInspectClose,
		HistoryOpen,
		QuerySavedOpen,
		QuerySavedDelete,
		QuerySave,
		QueryOpenFile,
		PlanToggle,
		PlanExpandAll,
		PlanCollapseAll,
		PlanJumpHeaviest,
		PlanToggleRaw,
		PlanToggleInsights,
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
		OperatorDelete,
		OperatorYank,
		OperatorChange,
		OperatorUpper,
		OperatorLower,
		OperatorIndentRight,
		OperatorIndentLeft,
		OperatorDeleteEndOfLine,
		EditorPaste,
		EditorPasteBefore,
		EditorToggleCase,
		TextObjectInnerWord,
		TextObjectAroundWord,
		TextObjectInnerWORD,
		TextObjectAroundWORD,
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
		ConnectionManagerQuitOrClose,
		ConnectionManagerOpen,
		ConnectionManagerClose,
		ConnectionManagerDown,
		ConnectionManagerUp,
		ConnectionManagerConfirm,
		ConnectionManagerRetry,
		ConnectionManagerJumpFirst,
		ConnectionManagerJumpLast,
		ConnectionManagerAdd,
		ConnectionManagerEdit,
		ConnectionManagerFieldNext,
		ConnectionManagerFieldPrev,
		ConnectionManagerFieldEdit,
		ConnectionManagerToggle,
		ConnectionManagerDelete,
		ConnectionManagerPasteDSN,
		TipDismiss,
		ChangelogDismiss,
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
		CommitDialogTypeName,
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
		SettingsOpen,
		SettingsClose,
		SettingsNextTab,
		SettingsPrevTab,
		SettingsFieldUp,
		SettingsFieldDown,
		SettingsFieldEdit,
		SettingsFieldToggle,
		SettingsConfirm,
		SettingsKeybindingAdd,
		SettingsKeybindingDelete,
		FilePickerUp,
		FilePickerDown,
		FilePickerJumpFirst,
		FilePickerJumpLast,
		FilePickerConfirm,
		FilePickerAscend,
		FilePickerCancel,
		FilePickerSearch,
		FilePickerSearchNext,
		FilePickerSearchPrev,
		FilePickerHidden,
		FilePickerNewDir,
		FilePickerSort,
		FilePickerHome,
		FilePickerFocusInput,
	}
}
