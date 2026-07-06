package i18n

// TranslationSet holds the full collection of localized UI strings used across
// pgsavvy. Top-level fields cover labels and short messages; the Actions
// sub-struct collects user-invocable command names. All fields are populated
// with English defaults by EnglishTranslationSet; locale overlays loaded via
// LoadAndMerge replace individual fields while leaving omitted ones at their
// English values.
type TranslationSet struct {
	OpenTable        string
	TruncateTable    string
	DropTable        string
	DropTableTooltip string
	AreYouSure       string
	ConnectionLost   string
	QueryCancelled   string
	Rows             string
	NullValue        string

	// Toast and refusal messages.
	CredentialsMissing    string
	TableDataEditDeferred string
	TerminalTooSmall      string

	// Disabled-binding reasons surfaced by Matcher.Dispatch when a
	// Command's Disabled() predicate refuses execution. The exact
	// reason string is rendered in the toast via the "<action>:
	// <reason>" template (see pkg/gui/keys/matcher.go).
	DisabledByDriver     string
	DisabledNoLiveCancel string

	// First-run tip popup.
	FirstRunTipTitle string
	FirstRunTipBody  string

	// Empty-state hint shown inside the CONNECTIONS view when no connections
	// are configured.
	EmptyConnectionsHint string

	// Empty-state placeholders for the SCHEMAS/TABLES/COLUMNS/INDEXES side
	// rails when their item list is empty.
	EmptySchemasHint string
	EmptyTablesHint  string
	EmptyColumnsHint string
	EmptyIndexesHint string

	// Relationship-panel (<leader>gr) body labels + empty states. The
	// panel lists the focused row's foreign keys; these strings render the
	// section headers and the various empty / degraded states.
	RelationshipPanelOutboundHeader  string
	RelationshipPanelInboundHeader   string
	RelationshipPanelNoTab           string
	RelationshipPanelNoRelationships string
	RelationshipPanelMoreFmt         string
	RelationshipPanelNull            string
	// RelationshipPanelInboundNeedsPK is the muted note shown in place of the
	// inbound section when the result has no row identity (join/view/PK-less);
	// no inbound queries are issued in that case.
	RelationshipPanelInboundNeedsPK string
	// RelationshipPanelEstimatePending marks an inbound estimate that has not
	// resolved yet.
	RelationshipPanelEstimatePending string
	// RelationshipPanelEstimateError marks an inbound estimate that failed
	// (e.g. permission denied on the child table); the rest of the panel
	// survives.
	RelationshipPanelEstimateError string
	// RelationshipPanelExactError marks a focused inbound line whose on-demand
	// EXACT count failed for a non-timeout reason (e.g. permission denied on the
	// child table). A timeout instead silently keeps the ~estimate.
	RelationshipPanelExactError string
	// RelationshipPanelBreadcrumbSep joins the breadcrumb segments (the walked
	// jump path projected from the jump list + tab labels).
	RelationshipPanelBreadcrumbSep string
	// RelationshipPanelBreadcrumbEmpty is the muted breadcrumb line shown when
	// no jumps have been made yet (just the current tab, no prior path).
	RelationshipPanelBreadcrumbEmpty string

	// Confirmation popup shown when unhiding a previously hidden schema.
	UnhideConfirmationTitle string
	UnhideConfirmationBody  string

	// Validation errors raised by the connection editor.
	DuplicateConnectionName string
	InvalidDSN              string
	// SaveConnectionFailed is the inline form error stamped when persisting
	// an add/edit form to connections.yml fails.
	SaveConnectionFailed string

	// Inline visual tags.
	ReadOnlyTag string

	// Schemas pane titles.
	SchemasTitle             string
	SchemasTitleHiddenSuffix string
	SchemaHiddenSuffix       string

	// Credentials safety bundle (G3-G).
	PlaintextPasswordInProfileWarn string
	DSNInlinePassword              string
	KeyringPassphraseRequired      string

	// Status bar fragments.
	OptionsBarMore string

	// Mode banner labels shown in the status bar's first slot. Empty
	// strings hide the slot (LabelForMode never emits these wrapped in
	// the separator).
	ModeNormal          string
	ModeInsert          string
	ModeVisual          string
	ModeVisualLine      string
	ModeVisualBlock     string
	ModeOperatorPending string
	ModeCommand         string
	ModeReplace         string

	// Cheatsheet rendering fragments. The popup paints the legend once
	// at the top, then a section per (Mode, Tag).
	CheatsheetCurrentScopeTab string
	CheatsheetGlobalTab       string
	CheatsheetLegend          string
	CheatsheetEmpty           string

	// Per-category labels for the cheatsheet Tag→Category taxonomy
	// (see pkg/cheatsheet/categories.go). General is the catch-all
	// label for tags with no explicit category.
	CheatsheetCategoryEditing string
	CheatsheetCategoryQuery   string
	CheatsheetCategoryResults string
	CheatsheetCategoryCells   string
	CheatsheetCategorySession string
	CheatsheetCategoryGeneral string

	// NOTICE/WARNING UI strings. The toast format
	// strings accept a single %d argument (the per-run notice count);
	// the "first" and "subsequent" variants exist so localisations can
	// distinguish singular from plural phrasing if needed.
	NoticeToastFirst      string
	NoticeToastSubsequent string
	SeverityNotice        string
	SeverityWarning       string
	SeverityInfo          string

	// Settings-modal category tab labels.
	SettingsGen    string
	SettingsTheme  string
	SettingsUI     string
	SettingsEditor string
	SettingsQuery  string
	SettingsKeys   string

	// Settings modal toast messages.
	SettingsSaved            string
	SettingsSaveFailed       string
	SettingsValidationFailed string

	Actions ActionTranslations
}

// ActionTranslations holds labels for user-invocable commands referenced from
// menus, buttons, and the keymap binding registry.
type ActionTranslations struct {
	OpenTable   string
	RunQuery    string
	CancelQuery string

	// Query editor family.
	QueryRunAll         string
	QueryExplain        string
	QueryExplainAnalyze string
	QueryRunInNewTx     string
	QueryFormat         string

	// Transaction submenu.
	TxBegin               string
	TxCommit              string
	TxRollback            string
	TxSavepoint           string
	TxReleaseSavepoint    string
	TxRollbackToSavepoint string

	// Result-tab family.
	ResultTabJump   string
	ResultTabNext   string
	ResultTabPrev   string
	ResultTabClose  string
	ResultTabPin    string
	ResultTabCancel string

	// Result-grid pagination.
	ResultPageNext       string
	ResultPagePrev       string
	ResultReadToEnd      string
	ResultReadToEndForce string

	// In-grid search. Struct field names keep the historical
	// ResultFilter* form (the command-ID strings are unchanged); only the
	// display VALUES carry search semantics.
	ResultFilterPrompt string
	ResultFilterNext   string
	ResultFilterPrev   string
	ResultFilterClear  string
	ResultSearchAccept string
	ResultSearchCancel string

	// Left-rail (Schemas/Tables) highlight+jump search.
	RailSearchPrompt string
	RailSearchNext   string
	RailSearchPrev   string
	RailSearchClear  string

	// In-grid sort.
	ResultSortPick      string
	ResultSortPickLabel string

	// In-grid hide-columns overlay.
	ResultHideOverlay string

	// Result-export menu.
	ResultExportPrompt string
	ExportMenuUp       string
	ExportMenuDown     string
	ExportMenuLeft     string
	ExportMenuRight    string
	ExportMenuConfirm  string
	ExportMenuCancel   string
	ExportMenuEditPath string

	// Table-inspect popup.
	TableInspectOpen    string
	TableInspectNextTab string
	TableInspectPrevTab string
	TableInspectClose   string

	// History popup.
	HistoryOpen string

	// Saved-query picker popup.
	OpenSavedQueries string
	DeleteSavedQuery string
	SaveQuery        string

	// Relationship panel (<leader>gr FK-exploration sidebar).
	RelationshipPanelToggle string
	RelationshipPanelEnter  string
	RelationshipPanelExit   string
	RelationshipPanelDown   string
	RelationshipPanelUp     string

	// Expanded view mode + result-grid motion.
	ResultViewToggle      string
	ResultViewCellOpen    string
	ResultCursorDown      string
	ResultCursorUp        string
	ResultCursorLeft      string
	ResultCursorRight     string
	ResultColFirst        string
	ResultColLast         string
	ResultJumpFirst       string
	ResultJumpLast        string
	ResultHalfPageDown    string
	ResultHalfPageUp      string
	ResultWrappedLineDown string
	ResultWrappedLineUp   string
	ResultSelectRow       string
	ResultSelectBlock     string
	ResultSelectCell      string
	ResultYankCell        string
	ResultYankRow         string

	// Connection lifecycle.
	AddConnection  string
	OpenConnection string

	// Connection-manager form. The in-place add/edit form
	// rendered inside the CONNECTION_MANAGER modal.
	DeleteConnection string
	EditConnection   string
	ToggleField      string
	NextField        string
	PrevField        string

	// Schema visibility.
	HideSchema       string
	UnhideSchema     string
	ToggleShowHidden string

	// Reconnect.
	Reconnect string

	// PasteDSN populates the connection form's discrete fields from a DSN on
	// the clipboard.
	PasteDSN string

	// TestConnection dials the in-progress connection form and reports
	// pass/fail inline without establishing the real session.
	TestConnection string

	// SearchPathQuickSet.
	SearchPathQuickSet string

	// StatementTimeoutSet.
	StatementTimeoutSet string

	// Global app commands.
	QuitApp               string
	ShowMenu              string
	OpenConnectionManager string

	// Side rail navigation.
	RailSchemas     string
	RailTables      string
	RailQueryEditor string
	RailResults     string

	// Directional rail navigation. RailUp/RailDown describe
	// the Ctrl+K / Ctrl+J chords on the five side rails; RailLastRail
	// describes the QueryEditor's Ctrl+H "return to last rail" jump.
	RailUp       string
	RailDown     string
	RailLastRail string

	// RailTabNext / RailTabPrev describe the `]` / `[` SCHEMA_RAIL tab
	// cycle (Schemas ⇄ Tables, edge-wrapping).
	RailTabNext string
	RailTabPrev string

	// QueryRailTabNext / QueryRailTabPrev describe the `]` / `[` QUERY_RAIL
	// tab cycle (QueryEditor ⇄ SavedQuery ⇄ History, edge-wrapping).
	QueryRailTabNext string
	QueryRailTabPrev string

	// Cursor movement and confirmation primitives used by every side
	// rail controller. Added by T7a to satisfy the M11i rule
	// that every KeyBinding.Description sources from Tr.Actions.*.
	Down      string
	Up        string
	Confirm   string
	Cancel    string
	JumpFirst string
	JumpLast  string

	// Pan* describe the `h`/`l`/`0`/`$` rail horizontal-scroll bindings
	// that reveal names wider than the pane.
	PanLeft  string
	PanRight string
	PanStart string
	PanEnd   string

	// RefreshRail is the description for the `r` per-rail refresh
	// binding.
	RefreshRail string

	// Cell-viewer in-popup bindings.
	CellViewerScrollDown   string
	CellViewerScrollUp     string
	CellViewerHalfPageDown string
	CellViewerHalfPageUp   string
	CellViewerPageDown     string
	CellViewerPageUp       string
	CellViewerJumpTop      string
	CellViewerJumpBottom   string
	CellViewerScrollLeft   string
	CellViewerScrollRight  string
	CellViewerToggleWrap   string
	CellViewerTogglePretty string
	CellViewerYank         string
	CellViewerEdit         string
	CellViewerDismiss      string

	// Settings modal actions.
	SettingsOpen             string
	SettingsClose            string
	SettingsNextTab          string
	SettingsPrevTab          string
	SettingsFieldUp          string
	SettingsFieldDown        string
	SettingsFieldEdit        string
	SettingsFieldToggle      string
	SettingsConfirm          string
	SettingsKeybindingAdd    string
	SettingsKeybindingDelete string
}

// EnglishTranslationSet returns a freshly allocated TranslationSet populated
// with the English baseline. A new pointer (and new Actions value) is returned
// on every call so callers may safely mutate the result without disturbing
// other callers — this invariant is relied upon by LoadAndMerge.
func EnglishTranslationSet() *TranslationSet {
	return &TranslationSet{
		OpenTable:        "Open Table",
		TruncateTable:    "Truncate Table",
		DropTable:        "Drop Table",
		DropTableTooltip: "Permanently delete this table and all of its data.",
		AreYouSure:       "Are you sure?",
		ConnectionLost:   "Connection lost.",
		QueryCancelled:   "Query cancelled.",
		Rows:             "rows",
		NullValue:        "NULL",

		CredentialsMissing:    "Credentials are not available; the action was refused.",
		TableDataEditDeferred: "Table data editing is not yet available.",
		TerminalTooSmall:      "Terminal too small. Please resize the window to continue.",

		DisabledByDriver:     "The active driver does not support this action.",
		DisabledNoLiveCancel: "The active driver cannot cancel a running query.",

		FirstRunTipTitle: "Welcome to pgsavvy",
		FirstRunTipBody:  "Press ? at any time to see available keys. Press a to add your first connection.",

		EmptyConnectionsHint: "No connections yet.\nPress a to add",

		EmptySchemasHint: "(no schemas)",
		EmptyTablesHint:  "(select a schema)",
		EmptyColumnsHint: "(select a table)",
		EmptyIndexesHint: "(select a table)",

		RelationshipPanelOutboundHeader:  "Outbound (parents)",
		RelationshipPanelInboundHeader:   "Inbound (children)",
		RelationshipPanelNoTab:           "(no active result)",
		RelationshipPanelNoRelationships: "(no relationships)",
		RelationshipPanelMoreFmt:         "(+%d more)",
		RelationshipPanelNull:            "(null)",
		RelationshipPanelInboundNeedsPK:  "inbound needs a primary key",
		RelationshipPanelEstimatePending: "~…",
		RelationshipPanelEstimateError:   "~?",
		RelationshipPanelExactError:      "!?",
		RelationshipPanelBreadcrumbSep:   " -> ",
		RelationshipPanelBreadcrumbEmpty: "(no path)",

		UnhideConfirmationTitle: "Unhide schema?",
		UnhideConfirmationBody:  "⚠ This schema was previously hidden. Unhide and show it again?",

		DuplicateConnectionName: "A connection with that name already exists.",
		InvalidDSN:              "The DSN is not valid.",
		SaveConnectionFailed:    "Couldn't save the connection.",

		ReadOnlyTag: "[RO]",

		SchemasTitle:             "Schemas",
		SchemasTitleHiddenSuffix: " [+hidden]",
		SchemaHiddenSuffix:       " (hidden)",

		PlaintextPasswordInProfileWarn: "WARNING: storing plaintext passwords in the connection profile is insecure; prefer the system keyring.",
		DSNInlinePassword:              "DSN contains an inline password; please remove it and supply the password separately.",
		KeyringPassphraseRequired:      "A keyring passphrase is required; please unlock the keyring from your desktop session.",

		OptionsBarMore: "? -> More",

		ModeNormal:          "-- NORMAL --",
		ModeInsert:          "-- INSERT --",
		ModeVisual:          "-- VISUAL --",
		ModeVisualLine:      "-- V-LINE --",
		ModeVisualBlock:     "-- V-BLOCK --",
		ModeOperatorPending: "-- OPERATOR --",
		ModeCommand:         "-- COMMAND --",
		ModeReplace:         "-- REPLACE --",

		CheatsheetCurrentScopeTab: "Current context",
		CheatsheetGlobalTab:       "Global",
		CheatsheetLegend:          "·=default  ✱=override  ★=custom",
		CheatsheetEmpty:           "(no bindings)",

		CheatsheetCategoryEditing: "Editing",
		CheatsheetCategoryQuery:   "Query",
		CheatsheetCategoryResults: "Results",
		CheatsheetCategoryCells:   "Cells",
		CheatsheetCategorySession: "Session",
		CheatsheetCategoryGeneral: "General",

		NoticeToastFirst:      "Server NOTICE (%d)",
		NoticeToastSubsequent: "Server NOTICE (%d)",
		SeverityNotice:        "NOTICE",
		SeverityWarning:       "WARNING",
		SeverityInfo:          "INFO",

		SettingsGen:    "Gen",
		SettingsTheme:  "Theme",
		SettingsUI:     "UI",
		SettingsEditor: "Editor",
		SettingsQuery:  "Query",
		SettingsKeys:   "Keys",

		SettingsSaved:            "Settings saved",
		SettingsSaveFailed:       "Settings save failed",
		SettingsValidationFailed: "Validation failed",

		Actions: ActionTranslations{
			EditConnection: "Edit connection",
			ToggleField:    "Toggle field",
			NextField:      "Next field",
			PrevField:      "Previous field",

			OpenTable:   "Open Table",
			RunQuery:    "Run Query",
			CancelQuery: "Cancel Query",

			QueryRunAll:         "Run All Statements",
			QueryExplain:        "Explain",
			QueryExplainAnalyze: "Explain (analyze)",
			QueryRunInNewTx:     "Run in new transaction",
			QueryFormat:         "Format SQL",

			TxBegin:               "Begin transaction",
			TxCommit:              "Commit transaction",
			TxRollback:            "Rollback transaction",
			TxSavepoint:           "Create savepoint",
			TxReleaseSavepoint:    "Release savepoint",
			TxRollbackToSavepoint: "Rollback to savepoint",

			ResultTabJump:   "Jump to result tab",
			ResultTabNext:   "Next result tab",
			ResultTabPrev:   "Previous result tab",
			ResultTabClose:  "Close result tab",
			ResultTabPin:    "Pin / unpin result tab",
			ResultTabCancel: "Cancel result tab stream",

			ResultPageNext:       "Next result page",
			ResultPagePrev:       "Previous result page",
			ResultReadToEnd:      "Drain result to end",
			ResultReadToEndForce: "Drain result to end (force)",

			ResultFilterPrompt: "Search results",
			ResultFilterNext:   "Jump to next search match",
			ResultFilterPrev:   "Jump to previous search match",
			ResultFilterClear:  "Clear result search",
			ResultSearchAccept: "Accept search",
			ResultSearchCancel: "Cancel search",

			RailSearchPrompt: "Search rail",
			RailSearchNext:   "Jump to next rail match",
			RailSearchPrev:   "Jump to previous rail match",
			RailSearchClear:  "Clear rail search",

			ResultSortPick:      "Sort rows by column",
			ResultSortPickLabel: "sort by column",

			ResultHideOverlay: "Toggle column visibility",

			ResultExportPrompt: "Export result...",
			ExportMenuUp:       "Move field up",
			ExportMenuDown:     "Move field down",
			ExportMenuLeft:     "Previous value",
			ExportMenuRight:    "Next value",
			ExportMenuConfirm:  "Start export",
			ExportMenuCancel:   "Cancel",
			ExportMenuEditPath: "Edit path",

			TableInspectOpen:    "Open table inspect",
			TableInspectNextTab: "Next tab",
			TableInspectPrevTab: "Previous tab",
			TableInspectClose:   "Close",

			HistoryOpen: "History tab",

			OpenSavedQueries: "Saved queries tab",
			DeleteSavedQuery: "Delete saved query",
			SaveQuery:        "Save query",

			RelationshipPanelToggle: "Toggle relationship panel",
			RelationshipPanelEnter:  "Focus relationship panel",
			RelationshipPanelExit:   "Leave relationship panel",
			RelationshipPanelDown:   "Select next relationship",
			RelationshipPanelUp:     "Select previous relationship",

			ResultViewToggle:      "Toggle expanded view",
			ResultViewCellOpen:    "View cell contents",
			ResultCursorDown:      "Cursor down",
			ResultCursorUp:        "Cursor up",
			ResultCursorLeft:      "Cursor left",
			ResultCursorRight:     "Cursor right",
			ResultColFirst:        "Jump to first column",
			ResultColLast:         "Jump to last column",
			ResultJumpFirst:       "Jump to first row",
			ResultJumpLast:        "Jump to last row",
			ResultHalfPageDown:    "Half page down",
			ResultHalfPageUp:      "Half page up",
			ResultWrappedLineDown: "Next wrapped line",
			ResultWrappedLineUp:   "Previous wrapped line",
			ResultSelectRow:       "Visual row selection",
			ResultSelectBlock:     "Visual block selection",
			ResultSelectCell:      "Visual cell selection",
			ResultYankCell:        "Yank focused cell",
			ResultYankRow:         "Yank focused row (TSV)",

			DeleteConnection: "Delete connection",
			AddConnection:    "Add Connection",
			OpenConnection:   "Open Connection",

			HideSchema:       "Hide Schema",
			UnhideSchema:     "Unhide Schema",
			ToggleShowHidden: "Toggle Show Hidden",

			Reconnect:           "Reconnect",
			PasteDSN:            "Paste DSN as fields",
			TestConnection:      "Test connection",
			SearchPathQuickSet:  "Set search_path",
			StatementTimeoutSet: "Set statement timeout",

			QuitApp:               "Quit",
			ShowMenu:              "Show Menu",
			OpenConnectionManager: "Open Connection Manager",

			RailSchemas:     "Schemas",
			RailTables:      "Tables",
			RailQueryEditor: "Query Editor",
			RailResults:     "Results",

			RailUp:       "Previous rail",
			RailDown:     "Next rail",
			RailLastRail: "Last rail",

			RailTabNext: "Next tab",
			RailTabPrev: "Previous tab",

			QueryRailTabNext: "Next tab",
			QueryRailTabPrev: "Previous tab",

			Down:      "Down",
			Up:        "Up",
			Confirm:   "Select",
			Cancel:    "Cancel",
			JumpFirst: "Jump to first",
			JumpLast:  "Jump to last",

			PanLeft:  "Scroll left",
			PanRight: "Scroll right",
			PanStart: "Scroll to start",
			PanEnd:   "Scroll to end",

			RefreshRail: "Refresh",

			CellViewerScrollDown:   "Scroll down",
			CellViewerScrollUp:     "Scroll up",
			CellViewerHalfPageDown: "Half page down",
			CellViewerHalfPageUp:   "Half page up",
			CellViewerPageDown:     "Page down",
			CellViewerPageUp:       "Page up",
			CellViewerJumpTop:      "Jump to top",
			CellViewerJumpBottom:   "Jump to bottom",
			CellViewerScrollLeft:   "Scroll left",
			CellViewerScrollRight:  "Scroll right",
			CellViewerToggleWrap:   "Toggle wrap",
			CellViewerTogglePretty: "Toggle pretty-print",
			CellViewerYank:         "Yank cell contents",
			CellViewerEdit:         "Edit cell",
			CellViewerDismiss:      "Dismiss viewer",

			SettingsOpen:             "Open settings",
			SettingsClose:            "Close settings",
			SettingsNextTab:          "Next tab",
			SettingsPrevTab:          "Previous tab",
			SettingsFieldUp:          "Move up",
			SettingsFieldDown:        "Move down",
			SettingsFieldEdit:        "Edit field",
			SettingsFieldToggle:      "Toggle field",
			SettingsConfirm:          "Save settings",
			SettingsKeybindingAdd:    "Add keybinding",
			SettingsKeybindingDelete: "Delete keybinding",
		},
	}
}
