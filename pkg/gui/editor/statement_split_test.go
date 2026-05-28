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

// TestSplitStatementsStringLiteral verifies that a semicolon inside a
// string literal does NOT split the statement.
func TestSplitStatementsStringLiteral(t *testing.T) {
	got := SplitStatements("SELECT ';';")
	want := []string{"SELECT ';'"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("string-literal split = %#v, want %#v", got, want)
	}
}

func TestSplitStatementsDollarQuoting(t *testing.T) {
	got := SplitStatements("SELECT $$ a; b $$;")
	want := []string{"SELECT $$ a; b $$"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dollar-quoting = %#v, want %#v", got, want)
	}
}

func TestSplitStatementsLineComment(t *testing.T) {
	got := SplitStatements("SELECT 1 -- ; not a split\n; SELECT 2;")
	want := []string{"SELECT 1 -- ; not a split\n", " SELECT 2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("line-comment = %#v, want %#v", got, want)
	}
}

func TestSplitStatementsBlockComment(t *testing.T) {
	got := SplitStatements("SELECT /* ; */ 1;")
	want := []string{"SELECT /* ; */ 1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("block-comment = %#v, want %#v", got, want)
	}
}

func TestSplitStatementsMixed(t *testing.T) {
	input := "INSERT INTO t VALUES ('a;b'); SELECT 1;"
	got := SplitStatements(input)
	want := []string{"INSERT INTO t VALUES ('a;b')", " SELECT 1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mixed = %#v, want %#v", got, want)
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

func TestStatementAtStringLiteral(t *testing.T) {
	// Semicolon inside a string literal should not be a split point.
	buf := "SELECT ';'; SELECT 2;"
	// Cursor at byte offset 0 (inside the first statement).
	if got := StatementAt(buf, 0); got != "SELECT ';'" {
		t.Fatalf("StatementAt string-literal = %q, want %q", got, "SELECT ';'")
	}
}

func TestStatementRangeAtBasic(t *testing.T) {
	buf := "SELECT 1; SELECT 2;"
	start, end := StatementRangeAt(buf, 3) // inside "SELECT 1"
	if start != 0 || end != 8 {
		t.Fatalf("StatementRangeAt basic = (%d, %d), want (0, 8)", start, end)
	}
}

func TestStatementRangeAtOnSemicolon(t *testing.T) {
	buf := "SELECT 1; SELECT 2;"
	// Rune offset 8 is the ';' after "SELECT 1"
	start, end := StatementRangeAt(buf, 8)
	if start != 0 || end != 8 {
		t.Fatalf("StatementRangeAt on-semi = (%d, %d), want (0, 8)", start, end)
	}
}

func TestStatementRangeAtSecondStatement(t *testing.T) {
	buf := "SELECT 1; SELECT 2;"
	start, end := StatementRangeAt(buf, 12) // inside " SELECT 2"
	if start != 9 || end != 18 {
		t.Fatalf("StatementRangeAt second = (%d, %d), want (9, 18)", start, end)
	}
}
