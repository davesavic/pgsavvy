// Package builtin holds the bundled ThemeConfig presets shipped with dbsavvy.
package builtin

import "github.com/davesavic/pgsavvy/pkg/config"

// DefaultDark returns the built-in dark theme used when the user has not
// supplied a configuration. Every exported field is populated with a
// non-empty string so a drift-guard reflect walk in the theme tests can
// catch newly-added ThemeConfig fields that forget a default.
func DefaultDark() *config.ThemeConfig {
	return &config.ThemeConfig{
		ActiveBorder:    "yellow",
		InactiveBorder:  "gray",
		SelectedRowBg:   "#3a3a3a",
		SelectedRowFg:   "white",
		NullValueFg:     "red",
		NumericFg:       "magenta",
		StringFg:        "green",
		KeywordFg:       "blue",
		CommentFg:       "gray",
		IdentifierFg:    "white",
		OperatorFg:      "yellow",
		BackgroundBg:    "#1e1e1e",
		ForegroundFg:    "white",
		StatusBarBg:     "#2d2d2d",
		StatusBarFg:     "white",
		CommandLineBg:   "#1e1e1e",
		CommandLineFg:   "white",
		ErrorFg:         "red",
		WarningFg:       "yellow",
		SuccessFg:       "green",
		InfoFg:          "cyan",
		HintFg:          "gray",
		PopupBg:         "#2d2d2d",
		PopupFg:         "white",
		PopupBorder:     "cyan",
		MenuBg:          "#2d2d2d",
		MenuFg:          "white",
		MenuSelectedBg:  "cyan",
		MenuSelectedFg:  "black",
		TableHeaderBg:   "#3a3a3a",
		TableHeaderFg:   "white",
		TableRowAltBg:   "#262626",
		GutterFg:        "gray",
		LineNumberFg:    "gray",
		CursorBg:        "white",
		CursorFg:        "black",
		MatchHighlight:  "yellow",
		SearchHighlight: "yellow",
		CurSearch:       "black on yellow",
		DiffAddedFg:     "green",
		DiffRemovedFg:   "red",
		DiffChangedFg:   "yellow",
		PromptFg:        "yellow",
		DirtyCellBg:     "on #5a4410",
		WarnBorder:      "#d97757",
	}
}
