package config

import "time"

// UserConfig is the root configuration struct loaded from YAML.
type UserConfig struct {
	ConfigVersion int                `yaml:"config_version"`
	Theme         ThemeConfig        `yaml:"theme"`
	Leader        string             `yaml:"leader"`
	Timeout       time.Duration      `yaml:"timeout"`
	Keybindings   []KeybindingConfig `yaml:"keybindings"`
}

// KeybindingConfig describes a single user-defined keybinding.
type KeybindingConfig struct {
	Mode        string   `yaml:"mode"`
	Scope       string   `yaml:"scope"`
	Sequence    []string `yaml:"sequence"`
	ActionID    string   `yaml:"action_id"`
	Description string   `yaml:"description"`
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
		Leader:        "<space>",
		Timeout:       1 * time.Second,
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
			{Mode: "normal", Scope: "global", Sequence: []string{"<c-c>"}, ActionID: "app.quit", Description: "Quit"},
			{Mode: "normal", Scope: "global", Sequence: []string{"<leader>", "q"}, ActionID: "app.quit", Description: "Quit via leader"},
		},
	}
}
