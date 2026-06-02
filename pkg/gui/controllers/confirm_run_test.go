package controllers

import (
	"strings"
	"testing"
)

// TestConfirmSQLPreviewPreservesShortStatement locks the regression from
// dbsavvy-u6p7: the confirmation popup must not chop a normal-length
// statement at the dry-run table's 64-char cap.
func TestConfirmSQLPreviewPreservesShortStatement(t *testing.T) {
	sql := "update accounts set email = 'tash@test.com' where id = '019dd92a-7c1e-4f00-9b3a-aaaabbbbcccc'"
	got := confirmSQLPreview(sql)
	if got != sql {
		t.Fatalf("confirmSQLPreview truncated a %d-char statement:\n got = %q\nwant = %q", len(sql), got, sql)
	}
	if strings.Contains(got, "…") {
		t.Fatalf("confirmSQLPreview added an ellipsis to a short statement: %q", got)
	}
}

// TestConfirmSQLPreviewCollapsesWhitespace verifies newlines/indentation
// collapse to single spaces so the popup's word-wrap reflows cleanly.
func TestConfirmSQLPreviewCollapsesWhitespace(t *testing.T) {
	got := confirmSQLPreview("update t\n   set a = 1\nwhere b = 2")
	want := "update t set a = 1 where b = 2"
	if got != want {
		t.Fatalf("confirmSQLPreview = %q, want %q", got, want)
	}
}

// TestConfirmSQLPreviewCapsOversizedStatement keeps the [y]es/[n]o prompt
// on screen by bounding a pathologically long statement.
func TestConfirmSQLPreviewCapsOversizedStatement(t *testing.T) {
	got := confirmSQLPreview(strings.Repeat("x", 5000))
	r := []rune(got)
	if len(r) != 400 {
		t.Fatalf("confirmSQLPreview length = %d, want 400", len(r))
	}
	if r[len(r)-1] != '…' {
		t.Fatalf("confirmSQLPreview did not append an ellipsis when capping: %q", string(r[len(r)-5:]))
	}
}

// TestConfirmRunBodyMultiStatementShowsCount asserts the multi-statement
// branch is unchanged by the single-statement preview swap.
func TestConfirmRunBodyMultiStatementShowsCount(t *testing.T) {
	got := confirmRunBody([]string{"select 1", "select 2", "select 3"})
	want := "Execute 3 statements?"
	if got != want {
		t.Fatalf("confirmRunBody = %q, want %q", got, want)
	}
}
