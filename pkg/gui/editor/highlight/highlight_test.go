package highlight

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/theme"
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
		SelectedRowBg:   "#3a3a3a",
		SelectedRowFg:   "white",
		NullValueFg:     "red",
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
		DiffAddedFg:     "green",
		DiffRemovedFg:   "red",
		DiffChangedFg:   "yellow",
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
		SelectedRowBg:   "#3a3a3a",
		SelectedRowFg:   "white",
		NullValueFg:     "red",
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
		DiffAddedFg:     "green",
		DiffRemovedFg:   "red",
		DiffChangedFg:   "yellow",
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

func TestParseHex_6Digit(t *testing.T) {
	r, g, b, ok := parseHex("#ff8800")
	if !ok {
		t.Fatal("parseHex failed")
	}
	if r != 255 || g != 136 || b != 0 {
		t.Fatalf("got (%d,%d,%d), want (255,136,0)", r, g, b)
	}
}

func TestParseHex_3Digit(t *testing.T) {
	r, g, b, ok := parseHex("#f80")
	if !ok {
		t.Fatal("parseHex failed")
	}
	if r != 0xff || g != 0x88 || b != 0x00 {
		t.Fatalf("got (%d,%d,%d), want (255,136,0)", r, g, b)
	}
}

func TestParseHex_Invalid(t *testing.T) {
	_, _, _, ok := parseHex("#xyz")
	if ok {
		t.Fatal("expected failure for #xyz")
	}
}

func TestColorToSGRFg_Named(t *testing.T) {
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
	}
	for _, tc := range cases {
		got := colorToSGRFg(tc.name)
		if got != tc.want {
			t.Errorf("colorToSGRFg(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestColorToSGRFg_Empty(t *testing.T) {
	got := colorToSGRFg("")
	if got != "" {
		t.Errorf("colorToSGRFg(\"\") = %q, want \"\"", got)
	}
}

func TestColorToSGRFg_Unknown(t *testing.T) {
	got := colorToSGRFg("chartreuse")
	if got != "" {
		t.Errorf("colorToSGRFg(\"chartreuse\") = %q, want \"\"", got)
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
