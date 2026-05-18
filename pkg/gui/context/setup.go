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
// 17 entries (11 live + 6 stub) for the cases where ordered iteration is
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
	Suggestions  *SuggestionsContext

	// Live EXTRAS / GLOBAL / DISPLAY instances.
	CommandLog *CommandLogContext
	Global     *GlobalContext
	Limit      *LimitContext
	WhichKey   *WhichKeyContext

	// Stub instances for deferred Contexts; Layout filters these by
	// Kind == STUB so they never reach SetView.
	QueryEditor     *StubContext
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
		Suggestions: NewSuggestionsContext(NewBaseContext(BaseContextOpts{
			Key:      types.SUGGESTIONS,
			ViewName: string(types.SUGGESTIONS),
			Kind:     types.TEMPORARY_POPUP,
		}), deps),

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

		// Stubs for the five deferred Contexts. ViewName matches the
		// eventual layout slot so naming stays consistent when the real
		// Context lands; Kind == STUB keeps Layout from creating the
		// view.
		QueryEditor:     NewStubContext(types.QUERY_EDITOR, string(types.QUERY_EDITOR)),
		TableDataEditor: NewStubContext(types.TABLE_DATA_EDITOR, string(types.TABLE_DATA_EDITOR)),
		ResultGrid:      NewStubContext(types.RESULT_GRID, string(types.RESULT_GRID)),
		Plan:            NewStubContext(types.PLAN, string(types.PLAN)),
		History:         NewStubContext(types.HISTORY, string(types.HISTORY)),
	}
}

// Flatten returns every Context (live + stub) in a stable order. Order
// is: side rail (5) -> popups (4) -> extras/global/display (4) -> stubs
// (5). Total length is always 18.
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
		t.Suggestions,
		t.CommandLog,
		t.Global,
		t.Limit,
		t.WhichKey,
		t.QueryEditor,
		t.TableDataEditor,
		t.ResultGrid,
		t.Plan,
		t.History,
	}
}

// ByKey returns the Context registered under the given key, or nil when
// the key is unknown. Lookup is O(n) over Flatten() — 18 entries — which
// is cheaper than maintaining a separate map for this size.
func (t *ContextTree) ByKey(key types.ContextKey) types.IBaseContext {
	for _, c := range t.Flatten() {
		if c.GetKey() == key {
			return c
		}
	}
	return nil
}
