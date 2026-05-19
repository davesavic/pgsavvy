package editor

import (
	"reflect"
	"testing"
)

func TestSplitStatementsBasic(t *testing.T) {
	got := SplitStatements("SELECT 1; SELECT 2; ")
	want := []string{"SELECT 1", " SELECT 2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SplitStatements basic = %#v, want %#v", got, want)
	}
}

func TestSplitStatementsEmptyAndWhitespaceOnly(t *testing.T) {
	cases := map[string]string{
		"empty":       "",
		"spaces":      "   ",
		"semis-only":  "  ;  ;",
		"newline-mix": " \n ; \n ; \n ",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if got := SplitStatements(in); got != nil {
				t.Fatalf("SplitStatements(%q) = %#v, want nil", in, got)
			}
		})
	}
}

func TestSplitStatementsNoTrailingSemicolon(t *testing.T) {
	got := SplitStatements("SELECT 1\nSELECT 2")
	want := []string{"SELECT 1\nSELECT 2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("no-trailing-semi = %#v, want %#v", got, want)
	}
}

// TestSplitStatementsKnownLimitationStringLiteral documents the v1
// behaviour around ';' inside a string literal. The E9 SQL-aware
// splitter will change this expectation; until then SplitStatements
// splits at the inner semicolon.
func TestSplitStatementsKnownLimitationStringLiteral(t *testing.T) {
	got := SplitStatements("SELECT ';';")
	want := []string{"SELECT '", "'"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("string-literal split = %#v, want %#v (known limitation)", got, want)
	}
}

func TestStatementAtCursorOnSecondLine(t *testing.T) {
	buf := "SELECT 1;\nSELECT 2;"
	// Cursor at the 'S' of "SELECT 2": byte offset 10 in "SELECT 1;\nSELECT 2;"
	off := 10
	if got := StatementAt(buf, off); got != "SELECT 2" {
		t.Fatalf("StatementAt(%q, %d) = %q, want %q", buf, off, got, "SELECT 2")
	}
}

func TestStatementAtCursorOnFirstStatement(t *testing.T) {
	buf := "SELECT 1;\nSELECT 2;"
	off := 3 // inside "SELECT 1"
	if got := StatementAt(buf, off); got != "SELECT 1" {
		t.Fatalf("StatementAt cursor-on-first = %q, want %q", got, "SELECT 1")
	}
}

func TestStatementAtTrailingCursorAfterFinalSemicolon(t *testing.T) {
	buf := "SELECT 1;"
	off := len(buf) // cursor at the very end (just past the ';')
	if got := StatementAt(buf, off); got != "SELECT 1" {
		t.Fatalf("StatementAt trailing = %q, want %q", got, "SELECT 1")
	}
}

func TestStatementAtEmptyBuffer(t *testing.T) {
	if got := StatementAt("", 0); got != "" {
		t.Fatalf("StatementAt empty = %q, want \"\"", got)
	}
}

func TestStatementAtCursorOutOfRangeIsClamped(t *testing.T) {
	buf := "SELECT 1; SELECT 2"
	if got := StatementAt(buf, 9999); got != "SELECT 2" {
		t.Fatalf("StatementAt out-of-range = %q, want %q", got, "SELECT 2")
	}
	if got := StatementAt(buf, -5); got != "SELECT 1" {
		t.Fatalf("StatementAt negative = %q, want %q", got, "SELECT 1")
	}
}

