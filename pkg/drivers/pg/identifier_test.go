package pg

import "testing"

func TestQuoteIdent(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: `""`},
		{name: "normal", in: "users", want: `"users"`},
		{name: "with_double_quote", in: `we"ird`, want: `"we""ird"`},
		{name: "with_dot", in: "weird.name", want: `"weird.name"`},
		{name: "mixed_case_preserved", in: "MyTable", want: `"MyTable"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := QuoteIdent(tc.in)
			if got != tc.want {
				t.Fatalf("QuoteIdent(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestQuoteQualified(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		ident  string
		want   string
	}{
		{name: "qualified", schema: "public", ident: "users", want: `"public"."users"`},
		{name: "empty_schema_fallback", schema: "", ident: "users", want: `"users"`},
		{name: "quoted_in_schema_and_name", schema: `we"ird`, ident: `na"me`, want: `"we""ird"."na""me"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := QuoteQualified(tc.schema, tc.ident)
			if got != tc.want {
				t.Fatalf("QuoteQualified(%q,%q) = %q, want %q", tc.schema, tc.ident, got, tc.want)
			}
		})
	}
}
