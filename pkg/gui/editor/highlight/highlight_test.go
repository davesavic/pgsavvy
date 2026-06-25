package highlight

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/theme"
)

// applyTestTheme installs a known theme so assertions are deterministic.
func applyTestTheme(t *testing.T) {
	t.Helper()
	err := theme.Apply(&config.ThemeConfig{
		KeywordFg:    "blue",
		StringFg:     "green",
		CommentFg:    "gray",
		NumericFg:    "magenta",
		OperatorFg:   "yellow",
		IdentifierFg: "white",
		// Fill remaining required fields with plausible values.
		ActiveBorder:    "yellow",
		InactiveBorder:  "gray",
		NullValueFg:     "red",
		ErrorFg:         "red",
		WarningFg:       "yellow",
		SuccessFg:       "green",
		InfoFg:          "cyan",
		PopupBorder:     "cyan",
		TableHeaderFg:   "white",
		SearchHighlight: "yellow",
		PromptFg:        "yellow",
		DirtyCellBg:     "#4a3818",
		WarnBorder:      "#d97757",
	})
	if err != nil {
		t.Fatalf("theme.Apply: %v", err)
	}
}

// --- Highlight tests ---

func TestHighlight_Empty(t *testing.T) {
	got := Highlight("")
	if got != "" {
		t.Fatalf("Highlight(\"\") = %q, want \"\"", got)
	}
}

func TestHighlight_TrailingReset(t *testing.T) {
	applyTestTheme(t)
	got := Highlight("SELECT 1")
	if !strings.HasSuffix(got, "\x1b[0m") {
		t.Fatalf("Highlight output does not end with reset: %q", got)
	}
}

func TestHighlight_ContainsKeywordColor(t *testing.T) {
	applyTestTheme(t)
	got := Highlight("SELECT 1")
	// KeywordFg = "blue" -> SGR "34"
	if !strings.Contains(got, "\x1b[34m") {
		t.Fatalf("expected blue SGR (\\x1b[34m) for keyword in %q", got)
	}
}

func TestHighlight_ContainsStringColor(t *testing.T) {
	applyTestTheme(t)
	got := Highlight("SELECT 'hello'")
	// StringFg = "green" -> SGR "32"
	if !strings.Contains(got, "\x1b[32m") {
		t.Fatalf("expected green SGR (\\x1b[32m) for string in %q", got)
	}
}

func TestHighlight_ContainsCommentColor(t *testing.T) {
	applyTestTheme(t)
	got := Highlight("-- a comment")
	// CommentFg = "gray" -> SGR "90"
	if !strings.Contains(got, "\x1b[90m") {
		t.Fatalf("expected gray SGR (\\x1b[90m) for comment in %q", got)
	}
}

func TestHighlight_ContainsNumericColor(t *testing.T) {
	applyTestTheme(t)
	got := Highlight("SELECT 42")
	// NumericFg = "magenta" -> SGR "35"
	if !strings.Contains(got, "\x1b[35m") {
		t.Fatalf("expected magenta SGR (\\x1b[35m) for number in %q", got)
	}
}

func TestHighlight_PreservesPlainText(t *testing.T) {
	applyTestTheme(t)
	got := Highlight("SELECT 1")
	// Strip all ANSI sequences; remaining text should match input.
	plain := stripANSI(got)
	if plain != "SELECT 1" {
		t.Fatalf("stripped text = %q, want %q", plain, "SELECT 1")
	}
}

func TestHighlight_HexColor(t *testing.T) {
	err := theme.Apply(&config.ThemeConfig{
		KeywordFg:       "#ff8800",
		StringFg:        "green",
		CommentFg:       "gray",
		NumericFg:       "magenta",
		OperatorFg:      "yellow",
		IdentifierFg:    "white",
		ActiveBorder:    "yellow",
		InactiveBorder:  "gray",
		NullValueFg:     "red",
		ErrorFg:         "red",
		WarningFg:       "yellow",
		SuccessFg:       "green",
		InfoFg:          "cyan",
		PopupBorder:     "cyan",
		TableHeaderFg:   "white",
		SearchHighlight: "yellow",
		PromptFg:        "yellow",
		DirtyCellBg:     "#4a3818",
		WarnBorder:      "#d97757",
	})
	if err != nil {
		t.Fatalf("theme.Apply: %v", err)
	}

	got := Highlight("SELECT 1")
	// #ff8800 -> 38;2;255;136;0
	if !strings.Contains(got, "38;2;255;136;0") {
		t.Fatalf("expected true-color SGR for #ff8800 keyword in %q", got)
	}
}

// --- HighlightJSON tests ---

func TestHighlightJSON_Empty(t *testing.T) {
	got := HighlightJSON("")
	if got != "" {
		t.Fatalf("HighlightJSON(\"\") = %q, want \"\"", got)
	}
}

func TestHighlightJSON_TrailingReset(t *testing.T) {
	applyTestTheme(t)
	got := HighlightJSON(`{"key":"value"}`)
	if !strings.HasSuffix(got, "\x1b[0m") {
		t.Fatalf("HighlightJSON output does not end with reset: %q", got)
	}
}

func TestHighlightJSON_ContainsColor(t *testing.T) {
	applyTestTheme(t)
	got := HighlightJSON(`{"key":"value"}`)
	// StringFg = "green" -> SGR "32"
	if !strings.Contains(got, "\x1b[32m") {
		t.Fatalf("expected green SGR (\\x1b[32m) for JSON string in %q", got)
	}
}

func TestHighlightJSON_PreservesPlainText(t *testing.T) {
	applyTestTheme(t)
	input := `{"key":"value"}`
	got := HighlightJSON(input)
	plain := stripANSI(got)
	if plain != input {
		t.Fatalf("stripped text = %q, want %q", plain, input)
	}
}

func TestHighlightJSON_Monochrome(t *testing.T) {
	applyTestTheme(t)
	restore := theme.SetMonochromeForTest(true)
	defer restore()
	input := `{"key":"value"}`
	got := HighlightJSON(input)
	if got != input {
		t.Fatalf("HighlightJSON in monochrome mode = %q, want %q", got, input)
	}
}

func TestHighlightJSON_SizeGate(t *testing.T) {
	applyTestTheme(t)
	big := strings.Repeat("x", 1<<20+1)
	got := HighlightJSON(big)
	if !strings.HasSuffix(got, "\x1b[0m") {
		t.Fatalf("size-gated output must end with reset: %q", got)
	}
	if !strings.HasPrefix(got, big) {
		t.Fatalf("size-gated output must contain original text verbatim")
	}
}

func TestHighlightJSON_HexColor(t *testing.T) {
	err := theme.Apply(&config.ThemeConfig{
		KeywordFg:       "blue",
		StringFg:        "#ff8800",
		CommentFg:       "gray",
		NumericFg:       "magenta",
		OperatorFg:      "yellow",
		IdentifierFg:    "white",
		ActiveBorder:    "yellow",
		InactiveBorder:  "gray",
		NullValueFg:     "red",
		ErrorFg:         "red",
		WarningFg:       "yellow",
		SuccessFg:       "green",
		InfoFg:          "cyan",
		PopupBorder:     "cyan",
		TableHeaderFg:   "white",
		SearchHighlight: "yellow",
		PromptFg:        "yellow",
		DirtyCellBg:     "#4a3818",
		WarnBorder:      "#d97757",
	})
	if err != nil {
		t.Fatalf("theme.Apply: %v", err)
	}

	got := HighlightJSON(`{"key":"value"}`)
	// #ff8800 -> 38;2;255;136;0. JSON string values receive StringFg.
	if !strings.Contains(got, "38;2;255;136;0") {
		t.Fatalf("expected true-color SGR for #ff8800 string in %q", got)
	}
}

// --- Tokenize tests ---

func TestTokenize_Empty(t *testing.T) {
	got := Tokenize("")
	if got != nil {
		t.Fatalf("Tokenize(\"\") = %v, want nil", got)
	}
}

func TestTokenize_RuneOffsets(t *testing.T) {
	applyTestTheme(t)
	input := "SELECT 1"
	tokens := Tokenize(input)
	if len(tokens) == 0 {
		t.Fatal("Tokenize returned no tokens")
	}

	// Verify offsets are contiguous and cover the input.
	totalRunes := 0
	for i, tok := range tokens {
		if tok.RuneOffset != totalRunes {
			t.Fatalf("token[%d].RuneOffset = %d, want %d", i, tok.RuneOffset, totalRunes)
		}
		expectedLen := utf8.RuneCountInString(tok.Value)
		if tok.RuneLen != expectedLen {
			t.Fatalf("token[%d].RuneLen = %d, want %d (value=%q)", i, tok.RuneLen, expectedLen, tok.Value)
		}
		totalRunes += tok.RuneLen
	}
	if totalRunes != utf8.RuneCountInString(input) {
		t.Fatalf("total rune count = %d, want %d", totalRunes, utf8.RuneCountInString(input))
	}
}

func TestTokenize_Unicode(t *testing.T) {
	// Ensure rune offsets (not byte offsets) are used for multi-byte chars.
	input := "SELECT 'Ωmega'"
	tokens := Tokenize(input)
	if len(tokens) == 0 {
		t.Fatal("Tokenize returned no tokens")
	}

	totalRunes := 0
	for _, tok := range tokens {
		totalRunes += tok.RuneLen
	}
	if totalRunes != utf8.RuneCountInString(input) {
		t.Fatalf("total rune count = %d, want %d", totalRunes, utf8.RuneCountInString(input))
	}
}

func TestTokenize_ClassifiesKeyword(t *testing.T) {
	tokens := Tokenize("SELECT 1")
	if len(tokens) == 0 {
		t.Fatal("no tokens")
	}
	// First token should be SELECT -> Keyword.
	if tokens[0].Type != Keyword {
		t.Fatalf("tokens[0].Type = %v, want Keyword", tokens[0].Type)
	}
	if tokens[0].Value != "SELECT" {
		t.Fatalf("tokens[0].Value = %q, want %q", tokens[0].Value, "SELECT")
	}
}

func TestTokenize_ClassifiesComment(t *testing.T) {
	tokens := Tokenize("-- hello")
	if len(tokens) == 0 {
		t.Fatal("no tokens")
	}
	found := false
	for _, tok := range tokens {
		if tok.Type == Comment {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected at least one Comment token")
	}
}

func TestTokenize_ClassifiesString(t *testing.T) {
	tokens := Tokenize("SELECT 'abc'")
	found := false
	for _, tok := range tokens {
		if tok.Type == String {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected at least one String token")
	}
}

func TestTokenize_ClassifiesNumber(t *testing.T) {
	tokens := Tokenize("SELECT 42")
	found := false
	for _, tok := range tokens {
		if tok.Type == Number {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected at least one Number token")
	}
}

func TestTokenize_DollarQuotedString(t *testing.T) {
	tokens := Tokenize("SELECT $$body;here$$")
	for _, tok := range tokens {
		if tok.Type == Punctuation && tok.Value == ";" {
			t.Fatal("semicolon inside dollar-quoted string was tokenized as Punctuation")
		}
	}
	found := false
	for _, tok := range tokens {
		if tok.Type == String && strings.Contains(tok.Value, ";") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected dollar-quoted body containing ';' as a String token")
	}
}

// --- Internal helpers ---

// TestColorParamSGR_Named covers the foreground param numbers the highlighter
// composes for each named token via theme.ColorParamSGR(_, theme.Fg) (gray/grey
// alias to bright-black 90). It also covers the hex param path, which the
// unified resolver now owns.
func TestColorParamSGR_Named(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"black", "30"},
		{"red", "31"},
		{"green", "32"},
		{"yellow", "33"},
		{"blue", "34"},
		{"magenta", "35"},
		{"cyan", "36"},
		{"white", "37"},
		{"gray", "90"},
		{"grey", "90"},
		{"#ff8800", "38;2;255;136;0"}, // 6-digit hex (was TestParseHex_6Digit)
		{"#f80", "38;2;255;136;0"},    // 3-digit hex (was TestParseHex_3Digit)
	}
	for _, tc := range cases {
		got := theme.ColorParamSGR(tc.name, theme.Fg)
		if got != tc.want {
			t.Errorf("ColorParamSGR(%q, Fg) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestColorParamSGR_Empty(t *testing.T) {
	got := theme.ColorParamSGR("", theme.Fg)
	if got != "" {
		t.Errorf("ColorParamSGR(\"\", Fg) = %q, want \"\"", got)
	}
}

func TestColorParamSGR_Unknown(t *testing.T) {
	got := theme.ColorParamSGR("chartreuse", theme.Fg)
	if got != "" {
		t.Errorf("ColorParamSGR(\"chartreuse\", Fg) = %q, want \"\"", got)
	}
	if got := theme.ColorParamSGR("#xyz", theme.Fg); got != "" { // was TestParseHex_Invalid
		t.Errorf("ColorParamSGR(\"#xyz\", Fg) = %q, want \"\"", got)
	}
}

func TestStyleToSGR_BoldItalicUnderline(t *testing.T) {
	s := &theme.Style{Bold: true, Italic: true, Underline: true, Fg: "red"}
	got := styleToSGR(s)
	// Should contain 1 (bold), 3 (italic), 4 (underline), 31 (red fg)
	if !strings.Contains(got, "1;3;4;31") {
		t.Fatalf("styleToSGR = %q, expected 1;3;4;31", got)
	}
}

func TestStyleToSGR_Nil(t *testing.T) {
	got := styleToSGR(nil)
	if got != "" {
		t.Fatalf("styleToSGR(nil) = %q, want \"\"", got)
	}
}

func TestStyleToSGR_EmptyStyle(t *testing.T) {
	got := styleToSGR(&theme.Style{})
	if got != "" {
		t.Fatalf("styleToSGR(zero) = %q, want \"\"", got)
	}
}

// stripANSI removes all ANSI escape sequences from s.
func stripANSI(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			// Skip until 'm'.
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) {
				i = j + 1
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
