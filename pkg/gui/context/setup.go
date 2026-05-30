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
// Context by reference without re-lookup. Flatten() and ByKey() walk the
// flattened slice (built once by NewContextTree from the single
// contextSpecs table) for the cases where ordered iteration is preferable.
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

	// Connecting is the full-pane MAIN_CONTEXT connection-progress
	// screen pushed while a connection attempt is in flight (epic
	// dbsavvy-e53). When top of the focus stack it suppresses the
	// QueryEditor paint and occupies dims["main"].
	Connecting *ConnectingContext

	// Stub instances for the remaining deferred Contexts; Layout
	// filters these by Kind == STUB so they never reach SetView.
	TableDataEditor *StubContext
	ResultGrid      *StubContext
	Plan            *StubContext
	History         *StubContext

	// all is the flattened, ordered slice of every Context that
	// participates in Flatten()/ByKey() iteration. Built once by
	// NewContextTree from contextSpecs (entries with inFlatten=false —
	// Columns/Indexes — are assigned to their named field but excluded
	// here, mirroring the historical Flatten() contract).
	all []types.IBaseContext
}

// contextSpec is one row of the single source-of-truth wiring table
// (contextSpecs). It carries the BaseContextOpts data NewContextTree used
// to spell out per-context plus two closures: build constructs the
// concrete Context (capturing deps + any extra per-context deps), and
// assign stores the result into its named ContextTree field. Iterating
// this table replaces the hand-listed struct literal in NewContextTree and
// the hand-listed field walk in Flatten().
type contextSpec struct {
	key      types.ContextKey
	kind     types.ContextKind
	viewName string
	title    string
	// popupRect is the size-policy descriptor the orchestrator reads to
	// derive this context's Tier-3 popup rectangle. Zero value
	// (PopupSizeNone) for non-popup contexts and the overlay-rendered
	// LIMIT/WHICH_KEY. Replaces the hand-maintained popupRectFor switch.
	popupRect types.PopupRectSpec
	// inFlatten marks whether the built Context joins the flattened
	// iteration slice. False only for COLUMNS/INDEXES, which retain a
	// named field but are deferred (superseded by TABLE_INSPECT) and were
	// never part of the historical Flatten() output.
	inFlatten bool
	// build constructs the concrete Context from its BaseContext + deps.
	// Stubs ignore base and call NewStubContext; contexts needing extra
	// deps (CommandLine/WhichKey/Cheatsheet/QueryEditor) pull them off the
	// deps bag here.
	build func(base BaseContext, deps types.ContextTreeDeps) types.IBaseContext
	// assign stores the built Context into its named ContextTree field by
	// type-asserting back to the concrete type. Keeps the named fields the
	// canonical typed handles while construction stays data-driven.
	assign func(t *ContextTree, c types.IBaseContext)
}

// contextSpecs is the single source of truth for every Context the tree
// wires. Order defines Flatten() order: side rail -> temporary popups ->
// extras/global/display -> persistent popup -> main + stubs. COLUMNS and
// INDEXES sit beside their side-rail siblings as struct fields but carry
// inFlatten=false so they stay out of the flattened iteration (deferred,
// superseded by TABLE_INSPECT).
func contextSpecs() []contextSpec {
	return []contextSpec{
		// Side rail (Kind = SIDE_CONTEXT).
		{
			key: types.CONNECTIONS, kind: types.SIDE_CONTEXT, title: "Connections", inFlatten: true,
			build:  func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewConnectionsContext(b, d) },
			assign: func(t *ContextTree, c types.IBaseContext) { t.Connections = c.(*ConnectionsContext) },
		},
		{
			key: types.SCHEMAS, kind: types.SIDE_CONTEXT, title: "Schemas", inFlatten: true,
			build:  func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewSchemasContext(b, d) },
			assign: func(t *ContextTree, c types.IBaseContext) { t.Schemas = c.(*SchemasContext) },
		},
		{
			key: types.TABLES, kind: types.SIDE_CONTEXT, title: "Tables", inFlatten: true,
			build:  func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewTablesContext(b, d) },
			assign: func(t *ContextTree, c types.IBaseContext) { t.Tables = c.(*TablesContext) },
		},
		// COLUMNS/INDEXES: named fields retained, Kind=STUB, excluded from
		// Flatten() (superseded by TABLE_INSPECT popup, epic dbsavvy-3vf).
		{
			key: types.COLUMNS, kind: types.STUB, title: "Columns", inFlatten: false,
			build:  func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewColumnsContext(b, d) },
			assign: func(t *ContextTree, c types.IBaseContext) { t.Columns = c.(*ColumnsContext) },
		},
		{
			key: types.INDEXES, kind: types.STUB, title: "Indexes", inFlatten: false,
			build:  func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewIndexesContext(b, d) },
			assign: func(t *ContextTree, c types.IBaseContext) { t.Indexes = c.(*IndexesContext) },
		},

		// Popups (Kind = TEMPORARY_POPUP).
		{
			key: types.MENU, kind: types.TEMPORARY_POPUP, inFlatten: true,
			popupRect: types.PopupRectSpec{Kind: types.PopupSizeCentered, WidthFrac: 0.5, HeightFrac: 0.5},
			build:     func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewMenuContext(b, d) },
			assign:    func(t *ContextTree, c types.IBaseContext) { t.Menu = c.(*MenuContext) },
		},
		{
			key: types.CONFIRMATION, kind: types.TEMPORARY_POPUP, inFlatten: true,
			popupRect: types.PopupRectSpec{Kind: types.PopupSizeCentered, WidthFrac: 0.5, HeightFrac: 0.5},
			build:     func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewConfirmationContext(b, d) },
			assign:    func(t *ContextTree, c types.IBaseContext) { t.Confirmation = c.(*ConfirmationContext) },
		},
		{
			key: types.PROMPT, kind: types.TEMPORARY_POPUP, inFlatten: true,
			// 0.8 (not the generic 0.5) so wrapped validator-error bodies
			// don't truncate at the right edge (dbsavvy-8p5).
			popupRect: types.PopupRectSpec{Kind: types.PopupSizeCentered, WidthFrac: 0.8, HeightFrac: 0.5},
			build:     func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewPromptContext(b, d) },
			assign:    func(t *ContextTree, c types.IBaseContext) { t.Prompt = c.(*PromptContext) },
		},
		{
			key: types.SELECTION, kind: types.TEMPORARY_POPUP, inFlatten: true,
			popupRect: types.PopupRectSpec{Kind: types.PopupSizeCentered, WidthFrac: 0.5, HeightFrac: 0.5},
			build:     func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewSelectionContext(b, d) },
			assign:    func(t *ContextTree, c types.IBaseContext) { t.Selection = c.(*SelectionContext) },
		},
		{
			key: types.SUGGESTIONS, kind: types.TEMPORARY_POPUP, inFlatten: true,
			// Cursor-anchored dropdown (dbsavvy-etp.2): the orchestrator
			// places this below the editor cursor (flipping above near the
			// bottom edge), not screen-centred. WidthFrac/HeightFrac are
			// the centred fallback used when the editor view is absent.
			popupRect: types.PopupRectSpec{Kind: types.PopupSizeAnchored, WidthFrac: 0.5, HeightFrac: 0.5},
			build:     func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewSuggestionsContext(b, d) },
			assign:    func(t *ContextTree, c types.IBaseContext) { t.Suggestions = c.(*SuggestionsContext) },
		},
		{
			key: types.COMMAND_LINE, kind: types.TEMPORARY_POPUP, inFlatten: true,
			popupRect: types.PopupRectSpec{Kind: types.PopupSizeCommandLine},
			build: func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext {
				return NewCommandLineContext(b, d, d.ModeStore)
			},
			assign: func(t *ContextTree, c types.IBaseContext) { t.CommandLine = c.(*CommandLineContext) },
		},
		{
			key: types.HIDE_OVERLAY, kind: types.TEMPORARY_POPUP, inFlatten: true,
			popupRect: types.PopupRectSpec{Kind: types.PopupSizeCentered, WidthFrac: 0.5, HeightFrac: 0.5},
			build:     func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewHideOverlayContext(b, d) },
			assign:    func(t *ContextTree, c types.IBaseContext) { t.HideOverlay = c.(*HideOverlayContext) },
		},
		{
			key: types.EXPORT_MENU, kind: types.TEMPORARY_POPUP, inFlatten: true,
			popupRect: types.PopupRectSpec{Kind: types.PopupSizeCentered, WidthFrac: 0.5, HeightFrac: 0.5},
			build:     func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewExportMenuContext(b, d) },
			assign:    func(t *ContextTree, c types.IBaseContext) { t.ExportMenu = c.(*ExportMenuContext) },
		},
		{
			key: types.TABLE_INSPECT, kind: types.TEMPORARY_POPUP, title: "Table inspect", inFlatten: true,
			popupRect: types.PopupRectSpec{Kind: types.PopupSizeCentered, WidthFrac: 0.6, HeightFrac: 0.6},
			build:     func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewTableInspectContext(b, d) },
			assign:    func(t *ContextTree, c types.IBaseContext) { t.TableInspect = c.(*TableInspectContext) },
		},
		{
			key: types.CELL_EDITOR, kind: types.TEMPORARY_POPUP, title: "Cell editor", inFlatten: true,
			popupRect: types.PopupRectSpec{Kind: types.PopupSizeCellEditor},
			build:     func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewCellEditorContext(b, d) },
			assign:    func(t *ContextTree, c types.IBaseContext) { t.CellEditor = c.(*CellEditorContext) },
		},
		{
			key: types.COMMIT_DIALOG, kind: types.TEMPORARY_POPUP, title: "Commit", inFlatten: true,
			// 0.7 wide so the generated-SQL preview lines fit without
			// truncating (dbsavvy-b0l).
			popupRect: types.PopupRectSpec{Kind: types.PopupSizeCentered, WidthFrac: 0.7, HeightFrac: 0.6},
			build:     func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewCommitDialogContext(b, d) },
			assign:    func(t *ContextTree, c types.IBaseContext) { t.CommitDialog = c.(*CommitDialogContext) },
		},
		{
			key: types.CONFLICT_DIALOG, kind: types.TEMPORARY_POPUP, title: "Conflicts", inFlatten: true,
			popupRect: types.PopupRectSpec{Kind: types.PopupSizeCentered, WidthFrac: 0.6, HeightFrac: 0.6},
			build:     func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewConflictDialogContext(b, d) },
			assign:    func(t *ContextTree, c types.IBaseContext) { t.ConflictDialog = c.(*ConflictDialogContext) },
		},
		{
			key: types.FK_REVERSE_PICKER, kind: types.TEMPORARY_POPUP, title: "Reverse FK", inFlatten: true,
			popupRect: types.PopupRectSpec{Kind: types.PopupSizeCentered, WidthFrac: 0.6, HeightFrac: 0.6},
			build: func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext {
				return NewFKReversePickerContext(b, d)
			},
			assign: func(t *ContextTree, c types.IBaseContext) { t.FKReversePicker = c.(*FKReversePickerContext) },
		},

		// EXTRAS / GLOBAL / DISPLAY.
		{
			key: types.MESSAGES, kind: types.EXTRAS_CONTEXT, title: "Messages", inFlatten: true,
			build:  func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewMessagesContext(b, d) },
			assign: func(t *ContextTree, c types.IBaseContext) { t.Messages = c.(*MessagesContext) },
		},
		{
			// GLOBAL_CONTEXT has no view.
			key: types.GLOBAL, kind: types.GLOBAL_CONTEXT, viewName: "", inFlatten: true,
			build:  func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewGlobalContext(b, d) },
			assign: func(t *ContextTree, c types.IBaseContext) { t.Global = c.(*GlobalContext) },
		},
		{
			key: types.LIMIT, kind: types.DISPLAY_CONTEXT, inFlatten: true,
			build:  func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewLimitContext(b, d) },
			assign: func(t *ContextTree, c types.IBaseContext) { t.Limit = c.(*LimitContext) },
		},
		{
			key: types.WHICH_KEY, kind: types.DISPLAY_CONTEXT, inFlatten: true,
			build: func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext {
				return NewWhichKeyContext(b, d, d.WhichKey, d.WhichKeyRows)
			},
			assign: func(t *ContextTree, c types.IBaseContext) { t.WhichKey = c.(*WhichKeyContext) },
		},
		{
			key: types.CHEATSHEET, kind: types.DISPLAY_CONTEXT, inFlatten: true,
			popupRect: types.PopupRectSpec{Kind: types.PopupSizeCheatsheet},
			build: func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext {
				return NewCheatsheetContext(b, d, d.CheatsheetRender)
			},
			assign: func(t *ContextTree, c types.IBaseContext) { t.Cheatsheet = c.(*CheatsheetContext) },
		},

		// FirstRunTip is the welcome popup shown above CONNECTIONS on the
		// user's first launch (dbsavvy-56u.2). PERSISTENT_POPUP so
		// subsequent popup pushes don't auto-evict it.
		{
			key: types.FIRST_RUN_TIP, kind: types.PERSISTENT_POPUP, inFlatten: true,
			build:  func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewFirstRunTipContext(b, d) },
			assign: func(t *ContextTree, c types.IBaseContext) { t.FirstRunTip = c.(*FirstRunTipContext) },
		},

		// QUERY_EDITOR is the live top-right MAIN_CONTEXT pane (epic
		// dbsavvy-wwd). wwd.1 promotes it from stub to a real
		// BaseContext-embedding type; modes + matcher come straight from
		// the dependency bag so focus/blur can drive the ModeStore +
		// Matcher.Cancel contract documented on the type.
		{
			key: types.QUERY_EDITOR, kind: types.MAIN_CONTEXT, title: "Query Editor", inFlatten: true,
			build: func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext {
				return NewQueryEditorContext(b, d, d.ModeStore, d.Matcher)
			},
			assign: func(t *ContextTree, c types.IBaseContext) { t.QueryEditor = c.(*QueryEditorContext) },
		},

		// CONNECTING is the full-pane connection-progress MAIN_CONTEXT
		// (epic dbsavvy-e53). Modelled on QUERY_EDITOR: MAIN_CONTEXT kind,
		// inFlatten=true, view name "connecting". The layout pass paints it
		// into dims["main"] (suppressing QUERY_EDITOR) for the frame it is
		// top of the focus stack.
		{
			key: types.CONNECTING, kind: types.MAIN_CONTEXT, title: "Connecting", inFlatten: true,
			build:  func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewConnectingContext(b, d) },
			assign: func(t *ContextTree, c types.IBaseContext) { t.Connecting = c.(*ConnectingContext) },
		},

		// Stubs for the four remaining deferred Contexts. ViewName matches
		// the eventual layout slot so naming stays consistent when the real
		// Context lands; Kind == STUB keeps Layout from creating the view.
		{
			key: types.TABLE_DATA_EDITOR, kind: types.STUB, inFlatten: true,
			build: func(_ BaseContext, _ types.ContextTreeDeps) types.IBaseContext {
				return NewStubContext(types.TABLE_DATA_EDITOR, string(types.TABLE_DATA_EDITOR))
			},
			assign: func(t *ContextTree, c types.IBaseContext) { t.TableDataEditor = c.(*StubContext) },
		},
		{
			key: types.RESULT_GRID, kind: types.STUB, inFlatten: true,
			build: func(_ BaseContext, _ types.ContextTreeDeps) types.IBaseContext {
				return NewStubContext(types.RESULT_GRID, string(types.RESULT_GRID))
			},
			assign: func(t *ContextTree, c types.IBaseContext) { t.ResultGrid = c.(*StubContext) },
		},
		{
			key: types.PLAN, kind: types.STUB, inFlatten: true,
			build: func(_ BaseContext, _ types.ContextTreeDeps) types.IBaseContext {
				return NewStubContext(types.PLAN, string(types.PLAN))
			},
			assign: func(t *ContextTree, c types.IBaseContext) { t.Plan = c.(*StubContext) },
		},
		{
			key: types.HISTORY, kind: types.STUB, inFlatten: true,
			build: func(_ BaseContext, _ types.ContextTreeDeps) types.IBaseContext {
				return NewStubContext(types.HISTORY, string(types.HISTORY))
			},
			assign: func(t *ContextTree, c types.IBaseContext) { t.History = c.(*StubContext) },
		},
	}
}

// NewContextTree wires every live Context and every StubContext into a
// fresh registry by iterating contextSpecs (the single source of truth).
// deps is passed by value; individual Contexts retain it for their own
// hook invocations. Returns a non-nil pointer; safe to call with the zero
// ContextTreeDeps value (every hook is nil-safe).
func NewContextTree(deps types.ContextTreeDeps) *ContextTree {
	t := &ContextTree{}
	for _, spec := range contextSpecs() {
		viewName := spec.viewName
		if viewName == "" && spec.key != types.GLOBAL {
			// GLOBAL has an explicit empty ViewName; every other context's
			// view name matches its key (the historical default). GLOBAL's
			// spec.viewName is "" by design and must NOT fall back to the
			// key, so it is special-cased here.
			viewName = string(spec.key)
		}
		base := NewBaseContext(BaseContextOpts{
			Key:      spec.key,
			ViewName: viewName,
			Kind:     spec.kind,
			Title:    spec.title,
		})
		c := spec.build(base, deps)
		spec.assign(t, c)
		if spec.inFlatten {
			t.all = append(t.all, c)
		}
	}
	return t
}

// Flatten returns every Context (live + stub) in a stable order. Order is
// defined by contextSpecs (entries with inFlatten=true): side rail (3) ->
// temporary popups (13) -> extras/global/display (5) -> persistent popups
// (1) -> main + stubs (6). Total length is always 28 (COLUMNS/INDEXES are
// excluded, matching the historical contract).
func (t *ContextTree) Flatten() []types.IBaseContext {
	return t.all
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

// popupRectSpecByKey indexes each context's declared popup-rect
// descriptor by key. Built once from contextSpecs() (the closures are
// not invoked — only the static descriptor fields are read), so the
// orchestrator's popupRectFor can resolve a descriptor from a bare
// ContextKey without a constructed tree (the binding/wiring guard tests
// iterate keys only).
var popupRectSpecByKey = func() map[types.ContextKey]types.PopupRectSpec {
	m := make(map[types.ContextKey]types.PopupRectSpec)
	for _, s := range contextSpecs() {
		m[s.key] = s.popupRect
	}
	return m
}()

// PopupRectSpecFor returns the popup-rect descriptor declared for key, or
// the zero value (PopupSizeNone) when the key declares none.
func PopupRectSpecFor(key types.ContextKey) types.PopupRectSpec {
	return popupRectSpecByKey[key]
}
