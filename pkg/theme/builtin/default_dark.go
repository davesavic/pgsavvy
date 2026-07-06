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
		NullValue:       "red",
		Numeric:         "magenta",
		String:          "green",
		Keyword:         "blue",
		Comment:         "gray",
		Identifier:      "white",
		Operator:        "yellow",
		Error:           "red",
		Warning:         "yellow",
		Success:         "green",
		Info:            "cyan",
		PopupBorder:     "cyan",
		TableHeader:     "white",
		SearchHighlight: "yellow",
		CurSearch:       "black on yellow",
		Prompt:          "yellow",
		DirtyCell:       "#5a4410",
		WarnBorder:      "#d97757",
	}
}
