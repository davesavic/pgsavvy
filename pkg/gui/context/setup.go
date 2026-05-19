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
// 21 entries (16 live + 5 stub) for the cases where ordered iteration is
// preferable.
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

	// Live EXTRAS / GLOBAL / DISPLAY instances.
	CommandLog *CommandLogContext
	Global     *GlobalContext
	Limit      *LimitContext
	WhichKey   *WhichKeyContext
	Cheatsheet *CheatsheetContext

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
		}), deps),
		Schemas: NewSchemasContext(NewBaseContext(BaseContextOpts{
			Key:      types.SCHEMAS,
			ViewName: string(types.SCHEMAS),
			Kind:     types.SIDE_CONTEXT,
		}), deps),
		Tables: NewTablesContext(NewBaseContext(BaseContextOpts{
			Key:      types.TABLES,
			ViewName: string(types.TABLES),
			Kind:     types.SIDE_CONTEXT,
		}), deps),
		Columns: NewColumnsContext(NewBaseContext(BaseContextOpts{
			Key:      types.COLUMNS,
			ViewName: string(types.COLUMNS),
			Kind:     types.SIDE_CONTEXT,
		}), deps),
		Indexes: NewIndexesContext(NewBaseContext(BaseContextOpts{
			Key:      types.INDEXES,
			ViewName: string(types.INDEXES),
			Kind:     types.SIDE_CONTEXT,
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

		// EXTRAS / GLOBAL / DISPLAY.
		CommandLog: NewCommandLogContext(NewBaseContext(BaseContextOpts{
			Key:      types.LOG,
			ViewName: string(types.LOG),
			Kind:     types.EXTRAS_CONTEXT,
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
// is: side rail (5) -> popups (6) -> extras/global/display (5) -> stubs
// (5). Total length is always 21.
func (t *ContextTree) Flatten() []types.IBaseContext {
	return []types.IBaseContext{
		t.Connections,
		t.Schemas,
		t.Tables,
		t.Columns,
		t.Indexes,
		t.Menu,
		t.Confirmation,
		t.Prompt,
		t.Selection,
		t.Suggestions,
		t.CommandLine,
		t.CommandLog,
		t.Global,
		t.Limit,
		t.WhichKey,
		t.Cheatsheet,
		t.QueryEditor,
		t.TableDataEditor,
		t.ResultGrid,
		t.Plan,
		t.History,
	}
}

// ByKey returns the Context registered under the given key, or nil when
// the key is unknown. Lookup is O(n) over Flatten() — 19 entries — which
// is cheaper than maintaining a separate map for this size.
func (t *ContextTree) ByKey(key types.ContextKey) types.IBaseContext {
	for _, c := range t.Flatten() {
		if c.GetKey() == key {
			return c
		}
	}
	return nil
}
