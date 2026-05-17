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
}

// UIConfig groups settings that govern UI behaviour (vs. data /
// connection settings). Today it only carries the mouse-enabled toggle
// (dbsavvy-zro T7b mouse wiring); future epics may add scrollback,
// double-click TTL, etc.
type UIConfig struct {
	Mouse MouseConfig `yaml:"mouse"`
}

// MouseConfig controls the optional mouse wiring registered by the
// controllers at startup. When Enabled is false, the mouse-binding
// registration block is skipped entirely (per dbsavvy-zro AC).
type MouseConfig struct {
	Enabled bool `yaml:"enabled"`
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
	DiffAddedFg     string `yaml:"diff_added_fg"`
	DiffRemovedFg   string `yaml:"diff_removed_fg"`
	DiffChangedFg   string `yaml:"diff_changed_fg"`
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
			Mouse: MouseConfig{Enabled: true},
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
