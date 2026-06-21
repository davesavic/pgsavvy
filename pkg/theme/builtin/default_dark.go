// Package builtin holds the bundled ThemeConfig presets shipped with pgsavvy.
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
		NullValueFg:     "red",
		NumericFg:       "magenta",
		StringFg:        "green",
		KeywordFg:       "blue",
		CommentFg:       "gray",
		IdentifierFg:    "white",
		OperatorFg:      "yellow",
		ErrorFg:         "red",
		WarningFg:       "yellow",
		SuccessFg:       "green",
		InfoFg:          "cyan",
		PopupBorder:     "cyan",
		TableHeaderFg:   "white",
		SearchHighlight: "yellow",
		CurSearch:       "black on yellow",
		PromptFg:        "yellow",
		DirtyCellBg:     "on #5a4410",
		WarnBorder:      "#d97757",
	}
}
