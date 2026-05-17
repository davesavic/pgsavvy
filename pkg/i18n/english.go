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

	Actions ActionTranslations
}

// ActionTranslations holds labels for user-invocable commands referenced from
// menus, buttons, and the keymap binding registry.
type ActionTranslations struct {
	OpenTable   string
	RunQuery    string
	CancelQuery string
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
		Actions: ActionTranslations{
			OpenTable:   "Open Table",
			RunQuery:    "Run Query",
			CancelQuery: "Cancel Query",
		},
	}
}
