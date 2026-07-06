package context

import (
	"fmt"
	"strconv"
	"time"

	"github.com/davesavic/pgsavvy/pkg/config"
)

type settingsFieldKind int

const (
	settingsFieldText settingsFieldKind = iota
	settingsFieldToggle
)

// Exported field-kind constants for the settings controller.
const (
	SettingsFieldText   = settingsFieldText
	SettingsFieldToggle = settingsFieldToggle
)

type settingsField struct {
	label   string
	yamlKey string
	kind    settingsFieldKind
	getter  func(*config.UserConfig) string
	setter  func(*config.UserConfig, string) error
	hint    string
	isColor bool
}

// Label returns the field's display label.
func (f settingsField) Label() string { return f.label }

// Kind returns the field's kind (text or toggle).
func (f settingsField) Kind() settingsFieldKind { return f.kind }

type settingsFormState struct {
	fields     []settingsField
	focusedIdx int
	scrollOff  int
	errorText  string
}

func buildSettingsFields() [6][]settingsField {
	themeFields := []settingsField{
		{"active_border", "theme.active_border", settingsFieldText, themeGetter("ActiveBorder"), themeSetter("ActiveBorder"), "focused panel border", true},
		{"inactive_border", "theme.inactive_border", settingsFieldText, themeGetter("InactiveBorder"), themeSetter("InactiveBorder"), "unfocused panel border", true},
		{"null_value", "theme.null_value", settingsFieldText, themeGetter("NullValue"), themeSetter("NullValue"), "NULL value in results", true},
		{"numeric", "theme.numeric", settingsFieldText, themeGetter("Numeric"), themeSetter("Numeric"), "numeric value in results", true},
		{"string", "theme.string", settingsFieldText, themeGetter("String"), themeSetter("String"), "string value in results", true},
		{"keyword", "theme.keyword", settingsFieldText, themeGetter("Keyword"), themeSetter("Keyword"), "SQL keywords", true},
		{"comment", "theme.comment", settingsFieldText, themeGetter("Comment"), themeSetter("Comment"), "SQL comments", true},
		{"identifier", "theme.identifier", settingsFieldText, themeGetter("Identifier"), themeSetter("Identifier"), "SQL identifiers", true},
		{"operator", "theme.operator", settingsFieldText, themeGetter("Operator"), themeSetter("Operator"), "SQL operators", true},
		{"error", "theme.error", settingsFieldText, themeGetter("Error"), themeSetter("Error"), "error messages", true},
		{"warning", "theme.warning", settingsFieldText, themeGetter("Warning"), themeSetter("Warning"), "warning messages", true},
		{"success", "theme.success", settingsFieldText, themeGetter("Success"), themeSetter("Success"), "success messages", true},
		{"info", "theme.info", settingsFieldText, themeGetter("Info"), themeSetter("Info"), "info messages", true},
		{"popup_border", "theme.popup_border", settingsFieldText, themeGetter("PopupBorder"), themeSetter("PopupBorder"), "popup/dialog border", true},
		{"table_header", "theme.table_header", settingsFieldText, themeGetter("TableHeader"), themeSetter("TableHeader"), "result table column headers", true},
		{"search_highlight", "theme.search_highlight", settingsFieldText, themeGetter("SearchHighlight"), themeSetter("SearchHighlight"), "search match highlight", true},
		{"cur_search", "theme.cur_search", settingsFieldText, themeGetter("CurSearch"), themeSetter("CurSearch"), "current search match, e.g. 'black on yellow'", true},
		{"prompt", "theme.prompt", settingsFieldText, themeGetter("Prompt"), themeSetter("Prompt"), "status / prompt bar text", true},
		{"dirty_cell", "theme.dirty_cell", settingsFieldText, themeGetter("DirtyCell"), themeSetter("DirtyCell"), "modified cell background tint", true},
		{"warn_border", "theme.warn_border", settingsFieldText, themeGetter("WarnBorder"), themeSetter("WarnBorder"), "warning popup border", true},
	}

	return [6][]settingsField{
		// Tab 0: General
		{
			{
				"leader", "leader", settingsFieldText,
				func(c *config.UserConfig) string { return c.Leader },
				func(c *config.UserConfig, v string) error { c.Leader = v; return nil },
				"prefix for all keybindings", false,
			},
			{
				"local_leader", "local_leader", settingsFieldText,
				func(c *config.UserConfig) string { return c.LocalLeader },
				func(c *config.UserConfig, v string) error { c.LocalLeader = v; return nil },
				"prefix for buffer-local keys", false,
			},
			{
				"config_version", "config_version", settingsFieldText,
				func(c *config.UserConfig) string { return strconv.Itoa(c.ConfigVersion) },
				nil,
				"read-only", false,
			},
		},
		// Tab 1: Theme
		themeFields,
		// Tab 2: UI
		{
			{
				"mouse_enabled", "ui.mouse.enabled", settingsFieldToggle,
				func(c *config.UserConfig) string { return boolStr(c.UI.Mouse.Enabled) },
				func(c *config.UserConfig, v string) error {
					b, err := parseBool(v)
					if err != nil {
						return err
					}
					c.UI.Mouse.Enabled = b
					return nil
				},
				"enable mouse support", false,
			},
			{
				"double_click_ms", "ui.mouse.double_click_ms", settingsFieldText,
				func(c *config.UserConfig) string { return strconv.Itoa(c.UI.Mouse.DoubleClickMs) },
				func(c *config.UserConfig, v string) error {
					n, err := strconv.Atoi(v)
					if err != nil {
						return fmt.Errorf("must be an integer")
					}
					if n < 100 || n > 2000 {
						return fmt.Errorf("must be 100-2000")
					}
					c.UI.Mouse.DoubleClickMs = n
					return nil
				},
				"100-2000", false,
			},
			{
				"result_page_size", "ui.result_page_size", settingsFieldText,
				func(c *config.UserConfig) string { return strconv.Itoa(c.UI.ResultPageSize) },
				intSetter(func(c *config.UserConfig) *int { return &c.UI.ResultPageSize }),
				"default 200", false,
			},
			{
				"result_prefetch_rows", "ui.result_prefetch_rows", settingsFieldText,
				func(c *config.UserConfig) string { return strconv.Itoa(c.UI.ResultPrefetchRows) },
				intSetter(func(c *config.UserConfig) *int { return &c.UI.ResultPrefetchRows }),
				"default 50", false,
			},
			{
				"prefetch_threshold", "ui.prefetch_threshold", settingsFieldText,
				func(c *config.UserConfig) string { return strconv.Itoa(c.UI.PrefetchThreshold) },
				func(c *config.UserConfig, v string) error {
					n, err := strconv.Atoi(v)
					if err != nil {
						return fmt.Errorf("must be an integer")
					}
					if n < 0 {
						return fmt.Errorf("must be >= 0")
					}
					c.UI.PrefetchThreshold = n
					return nil
				},
				"default 25", false,
			},
			{
				"read_to_end_warn_threshold", "ui.read_to_end_warn_threshold", settingsFieldText,
				func(c *config.UserConfig) string { return strconv.FormatInt(c.UI.ReadToEndWarnThreshold, 10) },
				func(c *config.UserConfig, v string) error {
					n, err := strconv.ParseInt(v, 10, 64)
					if err != nil {
						return fmt.Errorf("must be an integer")
					}
					if n <= 0 {
						return fmt.Errorf("must be > 0")
					}
					c.UI.ReadToEndWarnThreshold = n
					return nil
				},
				"default 1_000_000", false,
			},
			{
				"export_buffered_row_warn", "ui.export.buffered_row_warn_threshold", settingsFieldText,
				func(c *config.UserConfig) string { return strconv.FormatInt(c.UI.Export.BufferedRowWarnThreshold, 10) },
				func(c *config.UserConfig, v string) error {
					n, err := strconv.ParseInt(v, 10, 64)
					if err != nil {
						return fmt.Errorf("must be an integer")
					}
					if n <= 0 {
						return fmt.Errorf("must be > 0")
					}
					c.UI.Export.BufferedRowWarnThreshold = n
					return nil
				},
				"warn when exporting > N rows", false,
			},
			{
				"export_clipboard_max_bytes", "ui.export.clipboard_max_bytes", settingsFieldText,
				func(c *config.UserConfig) string { return strconv.FormatInt(c.UI.Export.ClipboardMaxBytes, 10) },
				func(c *config.UserConfig, v string) error {
					n, err := strconv.ParseInt(v, 10, 64)
					if err != nil {
						return fmt.Errorf("must be an integer")
					}
					if n <= 0 || n > (1<<30) {
						return fmt.Errorf("must be > 0 and <= 1 GiB")
					}
					c.UI.Export.ClipboardMaxBytes = n
					return nil
				},
				"max bytes for clipboard export (<=1GiB)", false,
			},
			{
				"yank_format", "ui.result_grid.yank_format", settingsFieldText,
				func(c *config.UserConfig) string { return c.UI.ResultGrid.YankFormat },
				func(c *config.UserConfig, v string) error { c.UI.ResultGrid.YankFormat = v; return nil },
				"copied cell format (csv, tsv, json)", false,
			},
		},
		// Tab 3: Editor
		{
			{
				"autocomplete", "editor.autocomplete", settingsFieldToggle,
				func(c *config.UserConfig) string { return boolStr(c.Editor.Autocomplete) },
				func(c *config.UserConfig, v string) error {
					b, err := parseBool(v)
					if err != nil {
						return err
					}
					c.Editor.Autocomplete = b
					return nil
				},
				"enable SQL autocompletion", false,
			},
			{
				"autocomplete_alias", "editor.autocomplete_alias", settingsFieldToggle,
				func(c *config.UserConfig) string { return boolStr(c.Editor.AutocompleteAlias) },
				func(c *config.UserConfig, v string) error {
					b, err := parseBool(v)
					if err != nil {
						return err
					}
					c.Editor.AutocompleteAlias = b
					return nil
				},
				"suggest table aliases in completions", false,
			},
			{
				"fk_forward_limit", "editor.fk_forward_limit", settingsFieldText,
				func(c *config.UserConfig) string { return strconv.Itoa(c.Editor.FKForwardLimit) },
				intSetter(func(c *config.UserConfig) *int { return &c.Editor.FKForwardLimit }),
				"default 1000", false,
			},
		},
		// Tab 4: Query
		{
			{
				"default_statement_timeout", "query.default_statement_timeout", settingsFieldText,
				func(c *config.UserConfig) string {
					if c.Query.DefaultStatementTimeout == nil {
						return ""
					}
					return c.Query.DefaultStatementTimeout.String()
				},
				func(c *config.UserConfig, v string) error {
					if v == "" {
						c.Query.DefaultStatementTimeout = nil
						return nil
					}
					d, err := time.ParseDuration(v)
					if err != nil {
						return fmt.Errorf("invalid duration: %v", err)
					}
					c.Query.DefaultStatementTimeout = &d
					return nil
				},
				"e.g. 30s, 5m", false,
			},
		},
		// Tab 5: Keys
		nil,
	}
}

func themeGetter(field string) func(*config.UserConfig) string {
	return func(c *config.UserConfig) string {
		switch field {
		case "ActiveBorder":
			return c.Theme.ActiveBorder
		case "InactiveBorder":
			return c.Theme.InactiveBorder
		case "NullValue":
			return c.Theme.NullValue
		case "Numeric":
			return c.Theme.Numeric
		case "String":
			return c.Theme.String
		case "Keyword":
			return c.Theme.Keyword
		case "Comment":
			return c.Theme.Comment
		case "Identifier":
			return c.Theme.Identifier
		case "Operator":
			return c.Theme.Operator
		case "Error":
			return c.Theme.Error
		case "Warning":
			return c.Theme.Warning
		case "Success":
			return c.Theme.Success
		case "Info":
			return c.Theme.Info
		case "PopupBorder":
			return c.Theme.PopupBorder
		case "TableHeader":
			return c.Theme.TableHeader
		case "SearchHighlight":
			return c.Theme.SearchHighlight
		case "CurSearch":
			return c.Theme.CurSearch
		case "Prompt":
			return c.Theme.Prompt
		case "DirtyCell":
			return c.Theme.DirtyCell
		case "WarnBorder":
			return c.Theme.WarnBorder
		}
		return ""
	}
}

func themeSetter(field string) func(*config.UserConfig, string) error {
	return func(c *config.UserConfig, v string) error {
		switch field {
		case "ActiveBorder":
			c.Theme.ActiveBorder = v
		case "InactiveBorder":
			c.Theme.InactiveBorder = v
		case "NullValue":
			c.Theme.NullValue = v
		case "Numeric":
			c.Theme.Numeric = v
		case "String":
			c.Theme.String = v
		case "Keyword":
			c.Theme.Keyword = v
		case "Comment":
			c.Theme.Comment = v
		case "Identifier":
			c.Theme.Identifier = v
		case "Operator":
			c.Theme.Operator = v
		case "Error":
			c.Theme.Error = v
		case "Warning":
			c.Theme.Warning = v
		case "Success":
			c.Theme.Success = v
		case "Info":
			c.Theme.Info = v
		case "PopupBorder":
			c.Theme.PopupBorder = v
		case "TableHeader":
			c.Theme.TableHeader = v
		case "SearchHighlight":
			c.Theme.SearchHighlight = v
		case "CurSearch":
			c.Theme.CurSearch = v
		case "Prompt":
			c.Theme.Prompt = v
		case "DirtyCell":
			c.Theme.DirtyCell = v
		case "WarnBorder":
			c.Theme.WarnBorder = v
		}
		return nil
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func parseBool(v string) (bool, error) {
	switch v {
	case "true", "yes", "on", "1":
		return true, nil
	case "false", "no", "off", "0":
		return false, nil
	}
	return false, fmt.Errorf("must be true/false")
}

func intSetter(ptr func(*config.UserConfig) *int) func(*config.UserConfig, string) error {
	return func(c *config.UserConfig, v string) error {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("must be an integer")
		}
		if n <= 0 {
			return fmt.Errorf("must be > 0")
		}
		*ptr(c) = n
		return nil
	}
}
