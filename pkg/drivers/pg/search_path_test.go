package pg

import "testing"

func TestSearchPathStmt(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		want   string
	}{
		{"empty is a no-op", "", ""},
		{"plain schema", "sales", `SET search_path TO "sales", public`},
		{"mixed case is quoted verbatim", "Sales", `SET search_path TO "Sales", public`},
		{"embedded quote is doubled", `we"ird`, `SET search_path TO "we""ird", public`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := searchPathStmt(tt.schema); got != tt.want {
				t.Fatalf("searchPathStmt(%q) = %q, want %q", tt.schema, got, tt.want)
			}
		})
	}
}
