package types

import "testing"

// TestContextKeyDisplay asserts the snake_case context keys humanize into
// the readable labels the cheatsheet popup shows in its tab bar + scope
// banner (dbsavvy-quyg).
func TestContextKeyDisplay(t *testing.T) {
	cases := []struct {
		key  ContextKey
		want string
	}{
		{QUERY_EDITOR, "Query Editor"},
		{RESULT_GRID, "Result Grid"},
		{TABLES, "Tables"},
		{GLOBAL, "Global"},
		{TABLE_DATA_EDITOR, "Table Data Editor"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := tc.key.Display(); got != tc.want {
			t.Errorf("ContextKey(%q).Display() = %q, want %q", tc.key, got, tc.want)
		}
	}
}
