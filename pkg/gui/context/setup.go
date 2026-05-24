package context

import (
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// ContextTree is the registry of every Context the dbsavvy TUI knows
// about. It is distinct from gui.ContextTree (the focus-stack manager in
// pkg/gui/context_mgr.go): this struct is the SOURCE of Context
// instances, while the stack manager owns their ORDERING.
//
// Concrete Context fields are exposed by name so the gui bootstrap (T10)
// and the controller registration shim (T7) can target a specific
// Context by reference without re-lookup. Flatten() and ByKey() walk all
// 27 entries (22 live + 4 stub + 1 PERSISTENT_POPUP) for the cases where
// ordered iteration is preferable.
type ContextTree struct {
	// Live SIDE_CONTEXT instances.
	Connections *ConnectionsContext
	Schemas     *SchemasContext
	Tables      *TablesContext
	Columns     *ColumnsContext
	Indexes     *IndexesContext

	// Live TEMPORARY_POPUP instances.
	Menu         *MenuContext
	Confirmation *ConfirmationContext
	Prompt       *PromptContext
	Selection    *SelectionContext
	Suggestions  *SuggestionsContext
	CommandLine  *CommandLineContext
	// HideOverlay is the <leader>gH column-visibility overlay
	// (dbsavvy-uv0.6). TEMPORARY_POPUP kind.
	HideOverlay *HideOverlayContext
	// ExportMenu is the <leader>oe export-result menu
	// (dbsavvy-uv0.9). TEMPORARY_POPUP kind.
	ExportMenu *ExportMenuContext
	// TableInspect is the tabbed columns/indexes inspect popup
	// (epic dbsavvy-3vf). TEMPORARY_POPUP kind.
	TableInspect *TableInspectContext
	// CellEditor is the inline cell-edit mini-buffer popup
	// (epic dbsavvy-bwq A1). TEMPORARY_POPUP kind.
	CellEditor *CellEditorContext
	// CommitDialog is the pending-edit commit dialog (epic
	// dbsavvy-bwq A4). TEMPORARY_POPUP kind.
	CommitDialog *CommitDialogContext
	// ConflictDialog is the per-conflict refresh/overwrite dialog
	// (epic dbsavvy-bwq A4). TEMPORARY_POPUP kind.
	ConflictDialog *ConflictDialogContext
	// FKReversePicker is the reverse-FK referencing-table picker
	// (epic dbsavvy-bwq B6). TEMPORARY_POPUP kind.
	FKReversePicker *FKReversePickerContext

	// Live EXTRAS / GLOBAL / DISPLAY instances.
	Messages   *MessagesContext
	Global     *GlobalContext
	Limit      *LimitContext
	WhichKey   *WhichKeyContext
	Cheatsheet *CheatsheetContext

	// Live PERSISTENT_POPUP instances.
	FirstRunTip *FirstRunTipContext

	// QueryEditor is the live top-right MAIN_CONTEXT pane that hosts
	// the vim-style SQL editor (epic dbsavvy-wwd). Promoted from
	// StubContext in dbsavvy-wwd.1; subsequent child tasks fill in
	// the *editor.Buffer / *editor.RepeatStore behind it.
	QueryEditor *QueryEditorContext

	// Stub instances for the remaining deferred Contexts; Layout
	// filters these by Kind == STUB so they never reach SetView.
	TableDataEditor *StubContext
	ResultGrid      *StubContext
	Plan            *StubContext
	History         *StubContext
}

// NewContextTree wires every live Context and every StubContext into a
// fresh registry. deps is passed by value; individual Contexts retain it
// for their own hook invocations. Returns a non-nil pointer; safe to
// call with the zero ContextTreeDeps value (every hook is nil-safe).
func NewContextTree(deps types.ContextTreeDeps) *ContextTree {
	return &ContextTree{
		// Side rail (Kind = SIDE_CONTEXT).
		Connections: NewConnectionsContext(NewBaseContext(BaseContextOpts{
			Key:      types.CONNECTIONS,
			ViewName: string(types.CONNECTIONS),
			Kind:     types.SIDE_CONTEXT,
			Title:    "Connections",
		}), deps),
		Schemas: NewSchemasContext(NewBaseContext(BaseContextOpts{
			Key:      types.SCHEMAS,
			ViewName: string(types.SCHEMAS),
			Kind:     types.SIDE_CONTEXT,
			Title:    "Schemas",
		}), deps),
		Tables: NewTablesContext(NewBaseContext(BaseContextOpts{
			Key:      types.TABLES,
			ViewName: string(types.TABLES),
			Kind:     types.SIDE_CONTEXT,
			Title:    "Tables",
		}), deps),
		Columns: NewColumnsContext(NewBaseContext(BaseContextOpts{
			Key:      types.COLUMNS,
			ViewName: string(types.COLUMNS),
			Kind:     types.STUB,
			Title:    "Columns",
		}), deps),
		Indexes: NewIndexesContext(NewBaseContext(BaseContextOpts{
			Key:      types.INDEXES,
			ViewName: string(types.INDEXES),
			Kind:     types.STUB,
			Title:    "Indexes",
		}), deps),

		// Popups (Kind = TEMPORARY_POPUP).
		Menu: NewMenuContext(NewBaseContext(BaseContextOpts{
			Key:      types.MENU,
			ViewName: string(types.MENU),
			Kind:     types.TEMPORARY_POPUP,
		}), deps),
		Confirmation: NewConfirmationContext(NewBaseContext(BaseContextOpts{
			Key:      types.CONFIRMATION,
			ViewName: string(types.CONFIRMATION),
			Kind:     types.TEMPORARY_POPUP,
		}), deps),
		Prompt: NewPromptContext(NewBaseContext(BaseContextOpts{
			Key:      types.PROMPT,
			ViewName: string(types.PROMPT),
			Kind:     types.TEMPORARY_POPUP,
		}), deps),
		Selection: NewSelectionContext(NewBaseContext(BaseContextOpts{
			Key:      types.SELECTION,
			ViewName: string(types.SELECTION),
			Kind:     types.TEMPORARY_POPUP,
		}), deps),
		Suggestions: NewSuggestionsContext(NewBaseContext(BaseContextOpts{
			Key:      types.SUGGESTIONS,
			ViewName: string(types.SUGGESTIONS),
			Kind:     types.TEMPORARY_POPUP,
		}), deps),
		CommandLine: NewCommandLineContext(NewBaseContext(BaseContextOpts{
			Key:      types.COMMAND_LINE,
			ViewName: string(types.COMMAND_LINE),
			Kind:     types.TEMPORARY_POPUP,
		}), deps, deps.ModeStore),
		HideOverlay: NewHideOverlayContext(NewBaseContext(BaseContextOpts{
			Key:      types.HIDE_OVERLAY,
			ViewName: string(types.HIDE_OVERLAY),
			Kind:     types.TEMPORARY_POPUP,
		}), deps),
		ExportMenu: NewExportMenuContext(NewBaseContext(BaseContextOpts{
			Key:      types.EXPORT_MENU,
			ViewName: string(types.EXPORT_MENU),
			Kind:     types.TEMPORARY_POPUP,
		}), deps),
		TableInspect: NewTableInspectContext(NewBaseContext(BaseContextOpts{
			Key:      types.TABLE_INSPECT,
			ViewName: string(types.TABLE_INSPECT),
			Kind:     types.TEMPORARY_POPUP,
			Title:    "Table inspect",
		}), deps),
		CellEditor: NewCellEditorContext(NewBaseContext(BaseContextOpts{
			Key:      types.CELL_EDITOR,
			ViewName: string(types.CELL_EDITOR),
			Kind:     types.TEMPORARY_POPUP,
			Title:    "Cell editor",
		}), deps),
		CommitDialog: NewCommitDialogContext(NewBaseContext(BaseContextOpts{
			Key:      types.COMMIT_DIALOG,
			ViewName: string(types.COMMIT_DIALOG),
			Kind:     types.TEMPORARY_POPUP,
			Title:    "Commit",
		}), deps),
		ConflictDialog: NewConflictDialogContext(NewBaseContext(BaseContextOpts{
			Key:      types.CONFLICT_DIALOG,
			ViewName: string(types.CONFLICT_DIALOG),
			Kind:     types.TEMPORARY_POPUP,
			Title:    "Conflicts",
		}), deps),
		FKReversePicker: NewFKReversePickerContext(NewBaseContext(BaseContextOpts{
			Key:      types.FK_REVERSE_PICKER,
			ViewName: string(types.FK_REVERSE_PICKER),
			Kind:     types.TEMPORARY_POPUP,
			Title:    "Reverse FK",
		}), deps),

		// EXTRAS / GLOBAL / DISPLAY.
		Messages: NewMessagesContext(NewBaseContext(BaseContextOpts{
			Key:      types.MESSAGES,
			ViewName: string(types.MESSAGES),
			Kind:     types.EXTRAS_CONTEXT,
			Title:    "Messages",
		}), deps),
		Global: NewGlobalContext(NewBaseContext(BaseContextOpts{
			Key:      types.GLOBAL,
			ViewName: "", // GLOBAL_CONTEXT has no view.
			Kind:     types.GLOBAL_CONTEXT,
		}), deps),
		Limit: NewLimitContext(NewBaseContext(BaseContextOpts{
			Key:      types.LIMIT,
			ViewName: string(types.LIMIT),
			Kind:     types.DISPLAY_CONTEXT,
		}), deps),
		WhichKey: NewWhichKeyContext(NewBaseContext(BaseContextOpts{
			Key:      types.WHICH_KEY,
			ViewName: string(types.WHICH_KEY),
			Kind:     types.DISPLAY_CONTEXT,
		}), deps, deps.WhichKey, deps.WhichKeyRows),
		Cheatsheet: NewCheatsheetContext(NewBaseContext(BaseContextOpts{
			Key:      types.CHEATSHEET,
			ViewName: string(types.CHEATSHEET),
			Kind:     types.DISPLAY_CONTEXT,
		}), deps, deps.CheatsheetRender),

		// FirstRunTip is the welcome popup shown above CONNECTIONS on
		// the user's first launch (dbsavvy-56u.2). PERSISTENT_POPUP so
		// subsequent popup pushes don't auto-evict it.
		FirstRunTip: NewFirstRunTipContext(NewBaseContext(BaseContextOpts{
			Key:      types.FIRST_RUN_TIP,
			ViewName: string(types.FIRST_RUN_TIP),
			Kind:     types.PERSISTENT_POPUP,
		}), deps),

		// QUERY_EDITOR is the live top-right MAIN_CONTEXT pane (epic
		// dbsavvy-wwd). wwd.1 promotes it from stub to a real
		// BaseContext-embedding type; modes + matcher come straight
		// from the dependency bag so focus/blur can drive the
		// ModeStore + Matcher.Cancel contract documented on the
		// type. ViewName matches the layout slot.
		QueryEditor: NewQueryEditorContext(NewBaseContext(BaseContextOpts{
			Key:      types.QUERY_EDITOR,
			ViewName: string(types.QUERY_EDITOR),
			Kind:     types.MAIN_CONTEXT,
			Title:    "Query Editor",
		}), deps, deps.ModeStore, deps.Matcher),

		// Stubs for the four remaining deferred Contexts. ViewName
		// matches the eventual layout slot so naming stays
		// consistent when the real Context lands; Kind == STUB
		// keeps Layout from creating the view.
		TableDataEditor: NewStubContext(types.TABLE_DATA_EDITOR, string(types.TABLE_DATA_EDITOR)),
		ResultGrid:      NewStubContext(types.RESULT_GRID, string(types.RESULT_GRID)),
		Plan:            NewStubContext(types.PLAN, string(types.PLAN)),
		History:         NewStubContext(types.HISTORY, string(types.HISTORY)),
	}
}

// Flatten returns every Context (live + stub) in a stable order. Order
// is: side rail (3) -> temporary popups (13) -> extras/global/display (5)
// -> persistent popups (1) -> main + stubs (5). Total length is always 27.
func (t *ContextTree) Flatten() []types.IBaseContext {
	return []types.IBaseContext{
		t.Connections,
		t.Schemas,
		t.Tables,
		t.Menu,
		t.Confirmation,
		t.Prompt,
		t.Selection,
		t.Suggestions,
		t.CommandLine,
		t.HideOverlay,
		t.ExportMenu,
		t.TableInspect,
		t.CellEditor,
		t.CommitDialog,
		t.ConflictDialog,
		t.FKReversePicker,
		t.Messages,
		t.Global,
		t.Limit,
		t.WhichKey,
		t.Cheatsheet,
		t.FirstRunTip,
		t.QueryEditor,
		t.TableDataEditor,
		t.ResultGrid,
		t.Plan,
		t.History,
	}
}

// ByKey returns the Context registered under the given key, or nil when
// the key is unknown. Lookup is O(n) over Flatten() — 27 entries — which
// is cheaper than maintaining a separate map for this size.
func (t *ContextTree) ByKey(key types.ContextKey) types.IBaseContext {
	for _, c := range t.Flatten() {
		if c.GetKey() == key {
			return c
		}
	}
	return nil
}
