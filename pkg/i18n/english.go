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

	// Result-tab family (dbsavvy-66p.12).
	ResultTabJump   string
	ResultTabNext   string
	ResultTabPrev   string
	ResultTabClose  string
	ResultTabPin    string
	ResultTabCancel string

	// Result-grid pagination (dbsavvy-uv0.3).
	ResultPageNext  string
	ResultPagePrev  string
	ResultReadToEnd string

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

	// Connection lifecycle.
	AddConnection  string
	OpenConnection string

	// Schema visibility.
	HideSchema       string
	UnhideSchema     string
	ToggleShowHidden string

	// Global app commands.
	QuitApp  string
	ShowMenu string

	// Side rail navigation.
	RailSchemas     string
	RailTables      string
	RailColumns     string
	RailIndexes     string
	RailQueryEditor string

	// Cursor movement and confirmation primitives used by every side
	// rail controller. Added by T7a (enn.8) to satisfy the M11i rule
	// that every KeyBinding.Description sources from Tr.Actions.*.
	Down    string
	Up      string
	Confirm string
	Cancel  string
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

			ResultTabJump:   "Jump to result tab",
			ResultTabNext:   "Next result tab",
			ResultTabPrev:   "Previous result tab",
			ResultTabClose:  "Close result tab",
			ResultTabPin:    "Pin / unpin result tab",
			ResultTabCancel: "Cancel result tab stream",

			ResultPageNext:  "Next result page",
			ResultPagePrev:  "Previous result page",
			ResultReadToEnd: "Drain result to end",

			ResultFilterPrompt:    "Filter rows by regex",
			ResultFilterToggleAll: "Toggle filter across all columns",
			ResultFilterNext:      "Jump to next filter match",
			ResultFilterPrev:      "Jump to previous filter match",
			ResultFilterClear:     "Clear result filter",

			ResultSortPick:      "Sort rows by column",
			ResultSortPickLabel: "sort by column",

			ResultHideOverlay: "Toggle column visibility",

			AddConnection:  "Add Connection",
			OpenConnection: "Open Connection",

			HideSchema:       "Hide Schema",
			UnhideSchema:     "Unhide Schema",
			ToggleShowHidden: "Toggle Show Hidden",

			QuitApp:  "Quit",
			ShowMenu: "Show Menu",

			RailSchemas:     "Schemas",
			RailTables:      "Tables",
			RailColumns:     "Columns",
			RailIndexes:     "Indexes",
			RailQueryEditor: "Query Editor",

			Down:    "Down",
			Up:      "Up",
			Confirm: "Select",
			Cancel:  "Cancel",
		},
	}
}
