package i18n

// TranslationSet holds the full collection of localized UI strings used across
// dbsavvy. Top-level fields cover labels and short messages; the Actions
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
	// rails when their item list is empty (dbsavvy-fow.5 U7).
	EmptySchemasHint string
	EmptyTablesHint  string
	EmptyColumnsHint string
	EmptyIndexesHint string

	// Confirmation popup shown when unhiding a previously hidden schema.
	UnhideConfirmationTitle string
	UnhideConfirmationBody  string

	// Validation errors raised by the connection editor.
	DuplicateConnectionName string
	InvalidDSN              string

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
	// at the top, then a section per (Mode, Tag). ScopeAllLabel renames
	// the "all" scope to a human-friendly label in cheatsheet output.
	CheatsheetTitle           string
	CheatsheetCurrentScopeTab string
	CheatsheetGlobalTab       string
	CheatsheetLegend          string
	CheatsheetEmpty           string
	CheatsheetScopeAllLabel   string

	// NOTICE/WARNING UI strings (dbsavvy-66p.13). The toast format
	// strings accept a single %d argument (the per-run notice count);
	// the "first" and "subsequent" variants exist so localisations can
	// distinguish singular from plural phrasing if needed.
	NoticeToastFirst      string
	NoticeToastSubsequent string
	SeverityNotice        string
	SeverityWarning       string
	SeverityInfo          string

	Actions ActionTranslations
}

// ActionTranslations holds labels for user-invocable commands referenced from
// menus, buttons, and the keymap binding registry.
type ActionTranslations struct {
	OpenTable   string
	RunQuery    string
	CancelQuery string

	// Query editor family (dbsavvy-66p.11).
	QueryRunAll         string
	QueryExplain        string
	QueryExplainAnalyze string
	QueryRunInNewTx     string

	// Transaction submenu (hq5.3).
	TxBegin               string
	TxCommit              string
	TxRollback            string
	TxSavepoint           string
	TxReleaseSavepoint    string
	TxRollbackToSavepoint string

	// Result-tab family (dbsavvy-66p.12).
	ResultTabJump   string
	ResultTabNext   string
	ResultTabPrev   string
	ResultTabClose  string
	ResultTabPin    string
	ResultTabCancel string

	// Result-grid pagination (dbsavvy-uv0.3).
	ResultPageNext       string
	ResultPagePrev       string
	ResultReadToEnd      string
	ResultReadToEndForce string

	// /regex in-grid filter (dbsavvy-uv0.4).
	ResultFilterPrompt    string
	ResultFilterToggleAll string
	ResultFilterNext      string
	ResultFilterPrev      string
	ResultFilterClear     string

	// In-grid sort (dbsavvy-uv0.5).
	ResultSortPick      string
	ResultSortPickLabel string

	// In-grid hide-columns overlay (dbsavvy-uv0.6).
	ResultHideOverlay string

	// Result-export menu (dbsavvy-uv0.9).
	ResultExportPrompt string
	ExportMenuUp       string
	ExportMenuDown     string
	ExportMenuLeft     string
	ExportMenuRight    string
	ExportMenuConfirm  string
	ExportMenuCancel   string

	// Table-inspect popup (dbsavvy-3vf).
	TableInspectOpen    string
	TableInspectNextTab string
	TableInspectPrevTab string
	TableInspectClose   string

	// Expanded view mode + result-grid motion (dbsavvy-uv0.7).
	ResultViewToggle      string
	ResultCursorDown      string
	ResultCursorUp        string
	ResultCursorLeft      string
	ResultCursorRight     string
	ResultJumpFirst       string
	ResultJumpLast        string
	ResultHalfPageDown    string
	ResultHalfPageUp      string
	ResultWrappedLineDown string
	ResultWrappedLineUp   string
	ResultSelectRow       string
	ResultSelectBlock     string
	ResultYankCell        string
	ResultYankRow         string

	// Connection lifecycle.
	AddConnection  string
	OpenConnection string

	// Schema visibility.
	HideSchema       string
	UnhideSchema     string
	ToggleShowHidden string

	// Reconnect (hq5.7).
	Reconnect string

	// SearchPathQuickSet (hq5.10).
	SearchPathQuickSet string

	// StatementTimeoutSet (hq5.11).
	StatementTimeoutSet string

	// Global app commands.
	QuitApp  string
	ShowMenu string

	// Side rail navigation.
	RailSchemas     string
	RailTables      string
	RailQueryEditor string
	RailResults     string

	// Directional rail navigation (dbsavvy-xs0). RailUp/RailDown describe
	// the Ctrl+K / Ctrl+J chords on the five side rails; RailLastRail
	// describes the QueryEditor's Ctrl+H "return to last rail" jump.
	RailUp       string
	RailDown     string
	RailLastRail string

	// Cursor movement and confirmation primitives used by every side
	// rail controller. Added by T7a (enn.8) to satisfy the M11i rule
	// that every KeyBinding.Description sources from Tr.Actions.*.
	Down    string
	Up      string
	Confirm string
	Cancel  string

	// RefreshRail is the description for the `r` per-rail refresh
	// binding (dbsavvy-56u.1).
	RefreshRail string
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

		FirstRunTipTitle: "Welcome to dbsavvy",
		FirstRunTipBody:  "Press ? at any time to see available keys. Press a to add your first connection.",

		EmptyConnectionsHint: "No connections yet.\nPress a to add",

		EmptySchemasHint: "(no schemas)",
		EmptyTablesHint:  "(select a schema)",
		EmptyColumnsHint: "(select a table)",
		EmptyIndexesHint: "(select a table)",

		UnhideConfirmationTitle: "Unhide schema?",
		UnhideConfirmationBody:  "⚠ This schema was previously hidden. Unhide and show it again?",

		DuplicateConnectionName: "A connection with that name already exists.",
		InvalidDSN:              "The DSN is not valid.",

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

		CheatsheetTitle:           "Keybindings",
		CheatsheetCurrentScopeTab: "Current context",
		CheatsheetGlobalTab:       "Global",
		CheatsheetLegend:          "·=default  ✱=override  ★=custom",
		CheatsheetEmpty:           "(no bindings)",
		CheatsheetScopeAllLabel:   "(non-popup)",

		NoticeToastFirst:      "Server NOTICE (%d)",
		NoticeToastSubsequent: "Server NOTICE (%d)",
		SeverityNotice:        "NOTICE",
		SeverityWarning:       "WARNING",
		SeverityInfo:          "INFO",

		Actions: ActionTranslations{
			OpenTable:   "Open Table",
			RunQuery:    "Run Query",
			CancelQuery: "Cancel Query",

			QueryRunAll:         "Run All Statements",
			QueryExplain:        "Explain",
			QueryExplainAnalyze: "Explain (analyze)",
			QueryRunInNewTx:     "Run in new transaction",

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

			ResultFilterPrompt:    "Filter rows by regex",
			ResultFilterToggleAll: "Toggle filter across all columns",
			ResultFilterNext:      "Jump to next filter match",
			ResultFilterPrev:      "Jump to previous filter match",
			ResultFilterClear:     "Clear result filter",

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

			TableInspectOpen:    "Open table inspect",
			TableInspectNextTab: "Next tab",
			TableInspectPrevTab: "Previous tab",
			TableInspectClose:   "Close",

			ResultViewToggle:      "Toggle expanded view",
			ResultCursorDown:      "Cursor down",
			ResultCursorUp:        "Cursor up",
			ResultCursorLeft:      "Cursor left",
			ResultCursorRight:     "Cursor right",
			ResultJumpFirst:       "Jump to first row",
			ResultJumpLast:        "Jump to last row",
			ResultHalfPageDown:    "Half page down",
			ResultHalfPageUp:      "Half page up",
			ResultWrappedLineDown: "Next wrapped line",
			ResultWrappedLineUp:   "Previous wrapped line",
			ResultSelectRow:       "Visual row selection",
			ResultSelectBlock:     "Visual block selection",
			ResultYankCell:        "Yank focused cell",
			ResultYankRow:         "Yank focused row (TSV)",

			AddConnection:  "Add Connection",
			OpenConnection: "Open Connection",

			HideSchema:       "Hide Schema",
			UnhideSchema:     "Unhide Schema",
			ToggleShowHidden: "Toggle Show Hidden",

			Reconnect:          "Reconnect",
			SearchPathQuickSet:  "Set search_path",
			StatementTimeoutSet: "Set statement timeout",

			QuitApp:  "Quit",
			ShowMenu: "Show Menu",

			RailSchemas:     "Schemas",
			RailTables:      "Tables",
			RailQueryEditor: "Query Editor",
			RailResults:     "Results",

			RailUp:       "Previous rail",
			RailDown:     "Next rail",
			RailLastRail: "Last rail",

			Down:    "Down",
			Up:      "Up",
			Confirm: "Select",
			Cancel:  "Cancel",

			RefreshRail: "Refresh",
		},
	}
}
