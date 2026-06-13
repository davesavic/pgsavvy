package editor

import "testing"

// TestSuggestionZeroValue pins the additive presentation-field contract
// (ko4m.4.1): a freshly constructed Suggestion has empty typed fields
// and false flags, so a bare-name suggestion renders unchanged (D6).
func TestSuggestionZeroValue(t *testing.T) {
	var s Suggestion

	if s.Kind != "" {
		t.Errorf("Kind = %q, want empty", s.Kind)
	}
	if s.Detail != "" {
		t.Errorf("Detail = %q, want empty", s.Detail)
	}
	if s.FKRef != "" {
		t.Errorf("FKRef = %q, want empty", s.FKRef)
	}
	if s.Signature != "" {
		t.Errorf("Signature = %q, want empty", s.Signature)
	}
	if s.Body != "" {
		t.Errorf("Body = %q, want empty", s.Body)
	}
	if s.IsPrimaryKey {
		t.Error("IsPrimaryKey = true, want false")
	}
	if s.NotNull {
		t.Error("NotNull = true, want false")
	}
}

// TestSanitizeSnippetText pins the snippet-body sanitizer contract
// (dbsavvy-ko4m.7.2): it PRESERVES '\n' and '\t' (a snippet expansion is a
// multi-line, indented insertion) while stripping every other C0 control
// byte (<0x20) and 0x7F. Contrast SanitizeText, which also strips '\n'/'\t'.
func TestSanitizeSnippetText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain", "SELECT * FROM t", "SELECT * FROM t"},
		{"preserves newline", "a\nb\nc", "a\nb\nc"},
		{"preserves tab", "a\tb", "a\tb"},
		{"preserves newline and tab together", "SELECT\n\t*\nFROM t", "SELECT\n\t*\nFROM t"},
		{"strips ESC", "a\x1bb", "ab"},
		{"strips NUL", "a\x00b", "ab"},
		{"strips DEL 0x7f", "a\x7fb", "ab"},
		{"strips CR keeps LF", "a\r\nb", "a\nb"},
		{"strips bell but keeps tab/newline", "a\x07\tb\nc", "a\tb\nc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizeSnippetText(tc.in); got != tc.want {
				t.Fatalf("SanitizeSnippetText(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSanitizeSnippetTextDiffersFromSanitizeText pins the contrast: the
// plain SanitizeText strips '\n' and '\t' whereas SanitizeSnippetText keeps
// them. Both strip control/DEL bytes.
func TestSanitizeSnippetTextDiffersFromSanitizeText(t *testing.T) {
	in := "a\nb\tc"
	if got := SanitizeText(in); got != "abc" {
		t.Fatalf("SanitizeText(%q) = %q; want %q", in, got, "abc")
	}
	if got := SanitizeSnippetText(in); got != in {
		t.Fatalf("SanitizeSnippetText(%q) = %q; want %q (newline+tab preserved)", in, got, in)
	}
}
