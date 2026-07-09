package context

import (
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// ContextTree is the registry of every Context the pgsavvy TUI knows
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
	// Live SIDE_CONTEXT instances. SchemaRail is the container that
	// multiplexes Schemas + Tables into the single "schemas-tables" view;
	// it is the only flattened side context for the consolidated rail.
	// Schemas/Tables retain typed fields but carry inFlatten=false (they
	// render only when SchemaRail calls them) and resolve GetViewName() to
	// "schemas-tables".
	SchemaRail *SchemaRailContext
	Schemas    *SchemasContext
	Tables     *TablesContext
	Columns    *ColumnsContext
	Indexes    *IndexesContext
	// ForeignKeys/Constraints are the remaining TABLE_INSPECT container tab
	// leaves (Kind=STUB, inFlatten=false): they retain typed fields but render
	// only when the container delegates to the active tab.
	ForeignKeys *ForeignKeysContext
	Constraints *ConstraintsContext

	// Live TEMPORARY_POPUP instances.
	Menu         *MenuContext
	Confirmation *ConfirmationContext
	Prompt       *PromptContext
	Selection    *SelectionContext
	Suggestions  *SuggestionsContext
	CommandLine  *CommandLineContext
	// SearchLine is the bottom-anchored in-grid search input.
	// TEMPORARY_POPUP kind, mirrors CommandLine.
	SearchLine *SearchLineContext
	// HideOverlay is the <leader>gH column-visibility overlay.
	// TEMPORARY_POPUP kind.
	HideOverlay *HideOverlayContext
	// ExportMenu is the <leader>oe export-result menu.
	// TEMPORARY_POPUP kind.
	ExportMenu *ExportMenuContext
	// TableInspect is the tabbed columns/indexes inspect popup.
	// TEMPORARY_POPUP kind.
	TableInspect *TableInspectContext
	// CellEditor is the inline cell-edit mini-buffer popup.
	// TEMPORARY_POPUP kind.
	CellEditor *CellEditorContext
	// CommitDialog is the pending-edit commit dialog.
	// TEMPORARY_POPUP kind.
	CommitDialog *CommitDialogContext
	// ConflictDialog is the per-conflict refresh/overwrite dialog.
	// TEMPORARY_POPUP kind.
	ConflictDialog *ConflictDialogContext
	// FKReversePicker is the reverse-FK referencing-table picker.
	// TEMPORARY_POPUP kind.
	FKReversePicker *FKReversePickerContext

	// Live GLOBAL / DISPLAY instances.
	Global     *GlobalContext
	Limit      *LimitContext
	WhichKey   *WhichKeyContext
	Cheatsheet *CheatsheetContext
	// RelationshipPanel is the <leader>gr right-docked FK-exploration
	// sidebar. DISPLAY_CONTEXT kind; renders right-anchored over the
	// result grid without stealing input focus.
	RelationshipPanel *RelationshipPanelContext

	// Live PERSISTENT_POPUP instances.
	FirstRunTip *FirstRunTipContext
	// CellViewer is the full cell-content viewer popup.
	// PERSISTENT_POPUP kind.
	CellViewer *CellViewerContext
	// Changelog is the post-upgrade release-notes popup.
	// PERSISTENT_POPUP kind.
	Changelog *ChangelogContext

	// FilePicker is the filesystem path picker popup.
	// TEMPORARY_POPUP kind.
	FilePicker *FilePickerContext

	// QueryRail is the MAIN_CONTEXT container that multiplexes the
	// QueryEditor + SavedQuery + History leaves into the single
	// "query_editor" view; it is the only flattened main context for the
	// consolidated query pane and the only entry pushed onto the focus
	// stack. The three leaves carry inFlatten=false and render only when
	// QueryRail calls the active leaf.
	QueryRail *QueryRailContext

	// QueryEditor is the live top-right MAIN_CONTEXT pane that hosts
	// the vim-style SQL editor. Promoted from StubContext; subsequent
	// child tasks fill in the *editor.Buffer / *editor.RepeatStore behind it.
	// Now a QUERY_RAIL container leaf (inFlatten=false): it renders only
	// when the container calls it and is never pushed onto the focus stack.
	QueryEditor *QueryEditorContext

	// ConnectionManager is the centered modal MAIN_CONTEXT connection
	// manager. When top of the focus stack it suppresses both
	// the side rails and the QueryEditor paint and renders a centered
	// bordered box over a blank background.
	ConnectionManager *ConnectionManagerContext

	// Settings is the 6-tabbed settings modal (MAIN_CONTEXT).
	Settings *SettingsContext

	// History is the <leader>h recent-query browser tab of the QUERY_RAIL
	// container. MAIN_CONTEXT leaf (inFlatten=false): rendered only when the
	// container calls it, never pushed onto the focus stack.
	History *HistoryContext

	// SavedQuery is the <leader>o saved-query picker tab of the QUERY_RAIL
	// container. MAIN_CONTEXT leaf (inFlatten=false): rendered only when the
	// container calls it, never pushed onto the focus stack.
	SavedQuery *SavedQueryContext

	// Stub instances for the remaining deferred Contexts; Layout
	// filters these by Kind == STUB so they never reach SetView.
	TableDataEditor *StubContext
	ResultGrid      *StubContext
	Plan            *StubContext

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
		// Side rail (Kind = SIDE_CONTEXT). SCHEMAS/TABLES are the leaves the
		// SCHEMA_RAIL container multiplexes: inFlatten=false (so the layout
		// Tier-1 loop never calls them directly — only the container does) and
		// viewName overridden to "schemas-tables" so HandleRender writes the
		// shared view. Their typed fields survive for the container + helpers.
		{
			key: types.SCHEMAS, kind: types.SIDE_CONTEXT, title: "Schemas", viewName: "schemas-tables", inFlatten: false,
			build:  func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewSchemasContext(b, d) },
			assign: func(t *ContextTree, c types.IBaseContext) { t.Schemas = c.(*SchemasContext) },
		},
		{
			key: types.TABLES, kind: types.SIDE_CONTEXT, title: "Tables", viewName: "schemas-tables", inFlatten: false,
			build:  func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewTablesContext(b, d) },
			assign: func(t *ContextTree, c types.IBaseContext) { t.Tables = c.(*TablesContext) },
		},
		// SCHEMA_RAIL container: the ONLY flattened side context for the
		// consolidated rail and the ONLY renderer of "schemas-tables". Ordered
		// after its leaves so the assign closure can inject them (t.Schemas /
		// t.Tables are set by the rows above). Non-editable: the master editor
		// for the SCHEMA_RAIL scope is constructed in installKeyDispatch and
		// attached to "schemas-tables" by the Tier-1 layout pass.
		{
			key: types.SCHEMA_RAIL, kind: types.SIDE_CONTEXT, title: "Schemas", viewName: "schemas-tables", inFlatten: true,
			build: func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewSchemaRailContext(b, d) },
			assign: func(t *ContextTree, c types.IBaseContext) {
				t.SchemaRail = c.(*SchemaRailContext)
				t.SchemaRail.SetLeaves(t.Schemas, t.Tables)
			},
		},
		// COLUMNS/INDEXES: named fields retained, Kind=STUB, excluded from
		// Flatten() (the TABLE_INSPECT container is the only renderer of the
		// shared view). viewName overridden to TableInspectViewName so their
		// HandleRender SetContent targets the SAME view the container's
		// SetViewTabs and the layout popup use — without it the leaf writes land
		// on a phantom "columns"/"indexes" view and the popup renders blank.
		{
			key: types.COLUMNS, kind: types.STUB, title: "Columns", viewName: TableInspectViewName, inFlatten: false,
			build:  func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewColumnsContext(b, d) },
			assign: func(t *ContextTree, c types.IBaseContext) { t.Columns = c.(*ColumnsContext) },
		},
		{
			key: types.INDEXES, kind: types.STUB, title: "Indexes", viewName: TableInspectViewName, inFlatten: false,
			build:  func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewIndexesContext(b, d) },
			assign: func(t *ContextTree, c types.IBaseContext) { t.Indexes = c.(*IndexesContext) },
		},
		{
			key: types.FOREIGN_KEYS, kind: types.STUB, title: "Foreign Keys", viewName: TableInspectViewName, inFlatten: false,
			build:  func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewForeignKeysContext(b, d) },
			assign: func(t *ContextTree, c types.IBaseContext) { t.ForeignKeys = c.(*ForeignKeysContext) },
		},
		{
			key: types.CONSTRAINTS, kind: types.STUB, title: "Constraints", viewName: TableInspectViewName, inFlatten: false,
			build:  func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewConstraintsContext(b, d) },
			assign: func(t *ContextTree, c types.IBaseContext) { t.Constraints = c.(*ConstraintsContext) },
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
			// Content-capped box: a single-field prompt sized
			// to its label+input, not a screen fraction. The cap is wide
			// enough that wrapped validator-error bodies don't
			// truncate at the right edge.
			popupRect: types.PopupRectSpec{Kind: types.PopupSizePrompt},
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
			// Cursor-anchored dropdown: the orchestrator
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
			key: types.SEARCH_LINE, kind: types.TEMPORARY_POPUP, inFlatten: true,
			popupRect: types.PopupRectSpec{Kind: types.PopupSizeCommandLine},
			build: func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext {
				return NewSearchLineContext(b, d, d.ModeStore)
			},
			assign: func(t *ContextTree, c types.IBaseContext) { t.SearchLine = c.(*SearchLineContext) },
		},
		{
			key: types.HIDE_OVERLAY, kind: types.TEMPORARY_POPUP, inFlatten: true,
			popupRect: types.PopupRectSpec{Kind: types.PopupSizeCentered, WidthFrac: 0.5, HeightFrac: 0.5},
			build:     func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewHideOverlayContext(b, d) },
			assign:    func(t *ContextTree, c types.IBaseContext) { t.HideOverlay = c.(*HideOverlayContext) },
		},
		{
			key: types.EXPORT_MENU, kind: types.TEMPORARY_POPUP, inFlatten: true, title: "Export result",
			popupRect: types.PopupRectSpec{Kind: types.PopupSizeCentered, WidthFrac: 0.5, HeightFrac: 0.5},
			build:     func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewExportMenuContext(b, d) },
			assign:    func(t *ContextTree, c types.IBaseContext) { t.ExportMenu = c.(*ExportMenuContext) },
		},
		// TABLE_INSPECT container: the ONLY renderer of TableInspectViewName.
		// viewName set explicitly (it would otherwise fall back to
		// string(key)) so the container's SetViewTabs + the leaves' SetContent
		// + the layout popup all target the same view. Ordered after its
		// COLUMNS/INDEXES leaves so the assign closure can inject them.
		{
			key: types.TABLE_INSPECT, kind: types.TEMPORARY_POPUP, title: "Table inspect", viewName: TableInspectViewName, inFlatten: true,
			popupRect: types.PopupRectSpec{Kind: types.PopupSizeCentered, WidthFrac: 0.6, HeightFrac: 0.6},
			build:     func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewTableInspectContext(b, d) },
			assign: func(t *ContextTree, c types.IBaseContext) {
				t.TableInspect = c.(*TableInspectContext)
				t.TableInspect.SetLeaves(t.Columns, t.Indexes, t.ForeignKeys, t.Constraints)
			},
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
			// truncating.
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
		{
			// HISTORY is now a QUERY_RAIL container leaf (tkt5.2 topology
			// flip): inFlatten=false, MAIN_CONTEXT, sharing the "query_editor"
			// view. It renders only when the container calls it and is never
			// pushed onto the focus stack. popupRect is dropped (it is no
			// longer a popup).
			key: types.HISTORY, kind: types.MAIN_CONTEXT, title: "History", viewName: QueryRailViewName, inFlatten: false,
			build: func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext {
				return NewHistoryContext(b, d)
			},
			assign: func(t *ContextTree, c types.IBaseContext) { t.History = c.(*HistoryContext) },
		},
		{
			// SAVED_QUERY is now a QUERY_RAIL container leaf (tkt5.2 topology
			// flip): inFlatten=false, MAIN_CONTEXT, sharing the "query_editor"
			// view. It drops its former PERSISTENT_POPUP kind + popupRect —
			// it is a normal rail leaf now, rendered only when the container
			// calls it and never pushed onto the focus stack.
			key: types.SAVED_QUERY, kind: types.MAIN_CONTEXT, title: "Saved Queries", viewName: QueryRailViewName, inFlatten: false,
			build: func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext {
				return NewSavedQueryContext(b, d)
			},
			assign: func(t *ContextTree, c types.IBaseContext) { t.SavedQuery = c.(*SavedQueryContext) },
		},

		// GLOBAL / DISPLAY.
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
			key: types.CHEATSHEET, kind: types.DISPLAY_CONTEXT, title: "Keybindings", inFlatten: true,
			popupRect: types.PopupRectSpec{Kind: types.PopupSizeCheatsheet},
			build: func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext {
				return NewCheatsheetContext(b, d)
			},
			assign: func(t *ContextTree, c types.IBaseContext) { t.Cheatsheet = c.(*CheatsheetContext) },
		},
		{
			// RELATIONSHIP_PANEL: the <leader>gr right-docked FK sidebar.
			// DISPLAY_CONTEXT so it renders via the Tier-3 stack loop; the
			// docked popup-rect anchors it over the rightmost slice of the
			// result-grid area. The grid keeps input focus (Tier-4 retains
			// the active tab view for this key).
			key: types.RELATIONSHIP_PANEL, kind: types.DISPLAY_CONTEXT, title: "Relationships", inFlatten: true,
			popupRect: types.PopupRectSpec{Kind: types.PopupSizeDocked, WidthFrac: 0.4},
			build: func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext {
				return NewRelationshipPanelContext(b, d)
			},
			assign: func(t *ContextTree, c types.IBaseContext) { t.RelationshipPanel = c.(*RelationshipPanelContext) },
		},

		// FirstRunTip is the welcome popup shown above CONNECTIONS on the
		// user's first launch. PERSISTENT_POPUP so
		// subsequent popup pushes don't auto-evict it.
		{
			key: types.FIRST_RUN_TIP, kind: types.PERSISTENT_POPUP, inFlatten: true,
			popupRect: types.PopupRectSpec{Kind: types.PopupSizeCentered, WidthFrac: 0.5, HeightFrac: 0.4},
			build:     func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewFirstRunTipContext(b, d) },
			assign:    func(t *ContextTree, c types.IBaseContext) { t.FirstRunTip = c.(*FirstRunTipContext) },
		},
		// CellViewer is the full cell-content viewer popup. PERSISTENT_POPUP
		// so it survives subsequent popup pushes (e.g. the cell editor on `i`).
		{
			key: types.CELL_VIEWER, kind: types.PERSISTENT_POPUP, title: "Cell viewer", inFlatten: true,
			popupRect: types.PopupRectSpec{Kind: types.PopupSizeCentered, WidthFrac: 0.7, HeightFrac: 0.7},
			build:     func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewCellViewerContext(b, d) },
			assign:    func(t *ContextTree, c types.IBaseContext) { t.CellViewer = c.(*CellViewerContext) },
		},
		// Changelog is the post-upgrade release-notes popup. PERSISTENT_POPUP
		// so it survives subsequent popup pushes.
		{
			key: types.CHANGELOG, kind: types.PERSISTENT_POPUP, title: "Changelog", inFlatten: true,
			popupRect: types.PopupRectSpec{Kind: types.PopupSizeCentered, WidthFrac: 0.7, HeightFrac: 0.8},
			build:     func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewChangelogContext(b, d) },
			assign:    func(t *ContextTree, c types.IBaseContext) { t.Changelog = c.(*ChangelogContext) },
		},
		// FilePicker is the filesystem path picker (TEMPORARY_POPUP, centered 60%).
		{
			key: types.FILE_PICKER, kind: types.TEMPORARY_POPUP, title: "File picker", inFlatten: true,
			popupRect: types.PopupRectSpec{Kind: types.PopupSizeCentered, WidthFrac: 0.6, HeightFrac: 0.6},
			build:     func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext { return NewFilePickerContext(b, d) },
			assign:    func(t *ContextTree, c types.IBaseContext) { t.FilePicker = c.(*FilePickerContext) },
		},

		// QUERY_EDITOR is now a QUERY_RAIL container leaf (tkt5.2 topology
		// flip): inFlatten=false so the layout Tier never calls it directly
		// (only the container does) and the master-editor build loop skips it
		// (relocated out-of-loop in installKeyDispatch). It keeps MAIN_CONTEXT
		// kind and the "query_editor" view; modes + matcher come straight from
		// the dependency bag so focus/blur can drive the ModeStore +
		// Matcher.Cancel contract documented on the type.
		{
			key: types.QUERY_EDITOR, kind: types.MAIN_CONTEXT, title: "Query Editor", viewName: QueryRailViewName, inFlatten: false,
			build: func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext {
				return NewQueryEditorContext(b, d, d.ModeStore, d.Matcher)
			},
			assign: func(t *ContextTree, c types.IBaseContext) { t.QueryEditor = c.(*QueryEditorContext) },
		},
		// QUERY_RAIL container: the ONLY flattened main context for the
		// consolidated query pane and the ONLY entry pushed onto the focus
		// stack for it. Ordered after its three leaves (QUERY_EDITOR /
		// SAVED_QUERY / HISTORY rows above) so the assign closure can inject
		// them positionally. Default active tab is 0 (the editor). Tab order:
		// editor (managesOwnOrigin), saved queries, history. Non-editable: the
		// master editor for QUERY_EDITOR/HISTORY/SAVED_QUERY scopes is built
		// out-of-loop in installKeyDispatch.
		{
			key: types.QUERY_RAIL, kind: types.MAIN_CONTEXT, title: "Query Editor", viewName: QueryRailViewName, inFlatten: true,
			build: func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext {
				return NewQueryRailContext(b, d,
					QueryRailTabSpec{Label: "Query Editor", LeafKey: types.QUERY_EDITOR, ManagesOwnOrigin: true},
					QueryRailTabSpec{Label: "Saved Queries", LeafKey: types.SAVED_QUERY},
					QueryRailTabSpec{Label: "History", LeafKey: types.HISTORY},
				)
			},
			assign: func(t *ContextTree, c types.IBaseContext) {
				t.QueryRail = c.(*QueryRailContext)
				t.QueryRail.SetLeaves(t.QueryEditor, t.SavedQuery, t.History)
			},
		},

		// CONNECTION_MANAGER is the centered modal MAIN_CONTEXT.
		// Modelled on CONNECTING: MAIN_CONTEXT kind, inFlatten=true, view name
		// "connection_manager". When top of the focus stack the layout pass
		// paints it as a centered bordered box, suppressing the side rails and
		// the QUERY_EDITOR for the frame.
		{
			key: types.CONNECTION_MANAGER, kind: types.MAIN_CONTEXT, title: "Connection Manager", inFlatten: true,
			build: func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext {
				return NewConnectionManagerContext(b, d)
			},
			assign: func(t *ContextTree, c types.IBaseContext) { t.ConnectionManager = c.(*ConnectionManagerContext) },
		},
		// SETTINGS is the 6-tabbed settings modal (MAIN_CONTEXT).
		{
			key: types.SETTINGS, kind: types.MAIN_CONTEXT, title: "Settings", inFlatten: true,
			build: func(b BaseContext, d types.ContextTreeDeps) types.IBaseContext {
				return NewSettingsContext(b, SettingsContextDeps{ContextTreeDeps: d})
			},
			assign: func(t *ContextTree, c types.IBaseContext) { t.Settings = c.(*SettingsContext) },
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
// defined by contextSpecs (entries with inFlatten=true): side rail (1 —
// the SCHEMA_RAIL container; SCHEMAS/TABLES are inFlatten=false leaves) ->
// temporary popups (13) -> global/display (4) -> persistent popups
// (1) -> main + stubs (6). SCHEMAS/TABLES/COLUMNS/INDEXES are excluded
// (named-field-only), matching the container/leaf contract.
func (t *ContextTree) Flatten() []types.IBaseContext {
	return t.all
}

// EditableViewNames returns the set of gocui view names that an editable
// context renders into. It consults the editable leaf fields directly (not
// Flatten) because QUERY_EDITOR is a non-flattened leaf of QUERY_RAIL.
//
// The orchestrator uses this to skip the non-editable Esc-abort shim on a
// view shared with an editable leaf (many-contexts-ONE-view topology): the
// leaf's master editor already delivers Escape to the Matcher, and gocui
// checks view keybindings before the Editor, so a shim there would shadow
// Escape.
func (t *ContextTree) EditableViewNames() map[string]bool {
	out := map[string]bool{}
	add := func(c types.IBaseContext, present bool) {
		if !present {
			return
		}
		if vn := c.GetViewName(); vn != "" {
			out[vn] = true
		}
	}
	add(t.QueryEditor, t.QueryEditor != nil)
	add(t.Prompt, t.Prompt != nil)
	add(t.CommandLine, t.CommandLine != nil)
	add(t.SearchLine, t.SearchLine != nil)
	add(t.CellEditor, t.CellEditor != nil)
	return out
}

// ByKey returns the Context registered under the given key, or nil when
// the key is unknown. Lookup is O(n) over Flatten() — 26 entries — which
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
