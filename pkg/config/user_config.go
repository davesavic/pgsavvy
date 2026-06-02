package config

import "time"

// UserConfig is the root configuration struct loaded from YAML.
type UserConfig struct {
	ConfigVersion int                `yaml:"config_version"`
	Theme         ThemeConfig        `yaml:"theme"`
	Leader        string             `yaml:"leader"`
	LocalLeader   string             `yaml:"local_leader"`
	Timeout       time.Duration      `yaml:"timeout"`
	TimeoutLen    time.Duration      `yaml:"timeout_len"`
	TtimeoutLen   time.Duration      `yaml:"ttimeout_len"`
	WhichKeyDelay time.Duration      `yaml:"whichkey_delay"`
	Keybindings   []KeybindingConfig `yaml:"keybindings"`
	UI            UIConfig           `yaml:"ui"`
	Editor        EditorConfig       `yaml:"editor"`
	Query         QueryConfig        `yaml:"query"`
}

// QueryConfig groups settings that govern query execution. dbsavvy-fow.7
// (U15) introduces the default statement-timeout ceiling applied to the
// streaming run path.
type QueryConfig struct {
	// DefaultStatementTimeout is the default ceiling applied to a streamed
	// query's context when the per-query models.Query.Timeout is zero.
	// 0 (the default) means OFF — no ceiling, the run path passes the
	// caller's context through unchanged. A non-zero per-query Timeout
	// always overrides this default. Must be >= 0. dbsavvy-fow.7 (U15).
	DefaultStatementTimeout time.Duration `yaml:"default_statement_timeout"`
}

// EditorConfig groups settings that govern the SQL editor behaviour.
// dbsavvy-bwq.22 (C5) introduces the Autocomplete toggle controlling
// auto-trigger of the completion popup. Manual `<c-x><c-o>` is not
// gated by this flag — it continues to fire regardless.
type EditorConfig struct {
	// Autocomplete enables auto-triggering of the completion popup when
	// the cursor sits after a recognised SQL context (e.g. trailing
	// `FROM `, `JOIN `, `<word>.`). Default true on fresh install.
	// Setting `editor.autocomplete: false` disables auto-trigger only;
	// the manual omni-complete chord remains available. (ADR-16)
	Autocomplete bool `yaml:"autocomplete"`

	// FKForwardLimit caps the row count of the parameterized SELECT
	// issued by the `gd` forward foreign-key navigation. Default 1000.
	// Must be > 0. dbsavvy-bwq.16 (B5).
	FKForwardLimit int `yaml:"fk_forward_limit"`
}

// UIConfig groups settings that govern UI behaviour (vs. data /
// connection settings). Today it carries the mouse-enabled toggle
// (dbsavvy-zro T7b mouse wiring) and the result-grid pagination knobs
// (dbsavvy-uv0.3). Future epics may add scrollback, double-click TTL, etc.
type UIConfig struct {
	Mouse MouseConfig `yaml:"mouse"`

	// ResultPageSize is the page size used by the ]p / [p result-grid
	// pagination chord. Default 200. Must be > 0.
	ResultPageSize int `yaml:"result_page_size"`

	// ResultPrefetchRows is the row count requested when the grid
	// cursor crosses PrefetchThreshold of the loaded tail. Default 50.
	// Must be > 0.
	ResultPrefetchRows int `yaml:"result_prefetch_rows"`

	// PrefetchThreshold is the distance from the loaded tail (in rows)
	// at which the View fires its near-tail prefetch callback. Default
	// 25. Must be >= 0.
	PrefetchThreshold int `yaml:"prefetch_threshold"`

	// ReadToEndWarnThreshold is the estimated-rows ceiling above which
	// G (ReadToEnd) shows a confirmation prompt before draining. Default
	// 1_000_000. Must be > 0.
	ReadToEndWarnThreshold int64 `yaml:"read_to_end_warn_threshold"`

	// FilterMaxRegexBytes caps the byte length of the /regex source
	// accepted by grid.View.SetFilter. Defense-in-depth against
	// pathological inputs. Default 4096. Must be > 0. dbsavvy-uv0.4.
	FilterMaxRegexBytes int `yaml:"filter_max_regex_bytes"`

	// Export carries the result-export knobs surfaced by the
	// <leader>oe menu. dbsavvy-uv0.9.
	Export ExportConfig `yaml:"export"`
}

// ExportConfig groups the user-tunable bounds for the result-export
// pipeline. The defaults are set in GetDefaultConfig and validated by
// ValidateUserConfig. dbsavvy-uv0.9.
type ExportConfig struct {
	// BufferedRowWarnThreshold is the buffered-row count above which
	// the export menu surfaces a "this will copy/write a lot of rows"
	// confirmation before proceeding. Default 100_000. Must be > 0.
	BufferedRowWarnThreshold int64 `yaml:"buffered_row_warn_threshold"`

	// ClipboardMaxBytes caps the payload size that may be pushed onto
	// the clipboard in a single export. Larger payloads must fall back
	// to a file destination. Default 16 MiB. Must be > 0 and <= 1 GiB.
	ClipboardMaxBytes int64 `yaml:"clipboard_max_bytes"`
}

// MouseConfig controls the optional mouse wiring registered by the
// controllers at startup. When Enabled is false, the mouse-binding
// registration block is skipped entirely (per dbsavvy-zro AC).
type MouseConfig struct {
	Enabled bool `yaml:"enabled"`

	// DoubleClickMs is the maximum gap (in milliseconds) between two
	// successive left-clicks on the same grid column header that still
	// counts as a double-click → SetSort invocation. Default 400; range
	// [100, 2000]. dbsavvy-uv0.5.
	DoubleClickMs int `yaml:"double_click_ms"`
}

// KeybindingConfig describes a single user-defined keybinding entry.
//
// Mode is "n" or a comma-separated subset of "n,i,v,V,<c-v>,o,x,c"
// (normal, insert, visual, visual-line, visual-block, operator-pending,
// command-line variants per the dbsavvy-dlp design).
//
// Scope is one of: a ContextKey value, "global", or "all".
//
// Key is a single sequence label (e.g. "<leader>tr", "gg", "<c-w>v")
// — the parser in ParseKeySequence splits it into tokens.
//
// Exactly one of Action or Command must be set (Action XOR Command).
// OriginFile/OriginLine are populated by the loader (not YAML); zero
// values are acceptable when no loader is involved.
type KeybindingConfig struct {
	Mode        string `yaml:"mode"`
	Scope       string `yaml:"scope"`
	Key         string `yaml:"key"`
	Action      string `yaml:"action,omitempty"`
	Command     string `yaml:"command,omitempty"`
	OpensMenu   bool   `yaml:"opens_menu,omitempty"`
	ShowInBar   bool   `yaml:"show_in_bar,omitempty"`
	Description string `yaml:"description,omitempty"`
	Tag         string `yaml:"tag,omitempty"`

	// Populated by the loader; not present in YAML.
	OriginFile string `yaml:"-"`
	OriginLine int    `yaml:"-"`
}

// ThemeConfig holds named color strings (color name or hex). Each field is
// parsed by pkg/theme.Apply into a concrete style; unrecognised values fall
// back to the default theme.
type ThemeConfig struct {
	ActiveBorder    string `yaml:"active_border"`
	InactiveBorder  string `yaml:"inactive_border"`
	SelectedRowBg   string `yaml:"selected_row_bg"`
	SelectedRowFg   string `yaml:"selected_row_fg"`
	NullValueFg     string `yaml:"null_value_fg"`
	NumericFg       string `yaml:"numeric_fg"`
	StringFg        string `yaml:"string_fg"`
	KeywordFg       string `yaml:"keyword_fg"`
	CommentFg       string `yaml:"comment_fg"`
	IdentifierFg    string `yaml:"identifier_fg"`
	OperatorFg      string `yaml:"operator_fg"`
	BackgroundBg    string `yaml:"background_bg"`
	ForegroundFg    string `yaml:"foreground_fg"`
	StatusBarBg     string `yaml:"status_bar_bg"`
	StatusBarFg     string `yaml:"status_bar_fg"`
	CommandLineBg   string `yaml:"command_line_bg"`
	CommandLineFg   string `yaml:"command_line_fg"`
	ErrorFg         string `yaml:"error_fg"`
	WarningFg       string `yaml:"warning_fg"`
	SuccessFg       string `yaml:"success_fg"`
	InfoFg          string `yaml:"info_fg"`
	HintFg          string `yaml:"hint_fg"`
	PopupBg         string `yaml:"popup_bg"`
	PopupFg         string `yaml:"popup_fg"`
	PopupBorder     string `yaml:"popup_border"`
	MenuBg          string `yaml:"menu_bg"`
	MenuFg          string `yaml:"menu_fg"`
	MenuSelectedBg  string `yaml:"menu_selected_bg"`
	MenuSelectedFg  string `yaml:"menu_selected_fg"`
	TableHeaderBg   string `yaml:"table_header_bg"`
	TableHeaderFg   string `yaml:"table_header_fg"`
	TableRowAltBg   string `yaml:"table_row_alt_bg"`
	GutterFg        string `yaml:"gutter_fg"`
	LineNumberFg    string `yaml:"line_number_fg"`
	CursorBg        string `yaml:"cursor_bg"`
	CursorFg        string `yaml:"cursor_fg"`
	MatchHighlight  string `yaml:"match_highlight"`
	SearchHighlight string `yaml:"search_highlight"`
	// CurSearch is the style for the CURRENT in-grid search match (the cell
	// the cursor sits on). Stronger than SearchHighlight so the active match
	// stands out from the others. dbsavvy-2ttm.2.
	CurSearch string `yaml:"cur_search"`
	DiffAddedFg     string `yaml:"diff_added_fg"`
	DiffRemovedFg   string `yaml:"diff_removed_fg"`
	DiffChangedFg   string `yaml:"diff_changed_fg"`
	PromptFg        string `yaml:"prompt_fg"`
	// DirtyCellBg is the background colour painted on grid cells that have
	// a staged PendingEdit. dbsavvy-bwq A3 (Z1 Phase A upstream).
	DirtyCellBg string `yaml:"dirty_cell_bg"`
	// WarnBorder is the popup border colour used by warning-themed
	// prompts (e.g. the free-form Expression prompt). dbsavvy-bwq A1 (Z1
	// Phase A upstream).
	WarnBorder string `yaml:"warn_border"`
}

// GetDefaultConfig returns the built-in default UserConfig. The returned
// value is a fresh pointer; callers may mutate it without affecting other
// callers.
func GetDefaultConfig() *UserConfig {
	return &UserConfig{
		ConfigVersion: 1,
		Leader:        " ",
		LocalLeader:   ",",
		Timeout:       1 * time.Second,
		TimeoutLen:    1 * time.Second,
		TtimeoutLen:   50 * time.Millisecond,
		WhichKeyDelay: 300 * time.Millisecond,
		Theme: ThemeConfig{
			ActiveBorder:   "white",
			InactiveBorder: "gray",
			SelectedRowBg:  "blue",
			NullValueFg:    "gray",
			NumericFg:      "cyan",
			StringFg:       "green",
			KeywordFg:      "magenta",
		},
		Keybindings: []KeybindingConfig{
			{Mode: "n", Scope: "global", Key: "<c-c>", Action: "app.quit", Description: "Quit"},
			{Mode: "n", Scope: "global", Key: "<leader>q", Action: "app.quit", Description: "Quit via leader"},
		},
		UI: UIConfig{
			Mouse:                  MouseConfig{Enabled: true, DoubleClickMs: 400},
			ResultPageSize:         200,
			ResultPrefetchRows:     50,
			PrefetchThreshold:      25,
			ReadToEndWarnThreshold: 1_000_000,
			FilterMaxRegexBytes:    4096,
			Export: ExportConfig{
				BufferedRowWarnThreshold: 100_000,
				ClipboardMaxBytes:        16 * 1024 * 1024,
			},
		},
		Editor: EditorConfig{
			Autocomplete:   true,
			FKForwardLimit: 1000,
		},
	}
}

// Sanitize applies SafeText to user-facing string fields on each
// keybinding (Description, Tag, Key) to strip control bytes that could
// corrupt the terminal. Callers SHOULD invoke this after YAML decode.
func (c *UserConfig) Sanitize() {
	if c == nil {
		return
	}
	for i := range c.Keybindings {
		c.Keybindings[i].Description = SafeText(c.Keybindings[i].Description)
		c.Keybindings[i].Tag = SafeText(c.Keybindings[i].Tag)
		c.Keybindings[i].Key = SafeText(c.Keybindings[i].Key)
	}
}
