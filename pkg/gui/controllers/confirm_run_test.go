package controllers

import (
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/theme"
)

// TestConfirmSQLPreviewPreservesShortStatement locks the regression:
// the confirmation popup must not chop a normal-length
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

// TestConfirmRunBodyMultiStatementListsStatements asserts the
// multi-statement branch keeps the count line and now lists each
// statement so the user sees what runs, not just a number. Monochrome
// is forced so the highlighter passes the SQL through verbatim.
func TestConfirmRunBodyMultiStatementListsStatements(t *testing.T) {
	restore := theme.SetMonochromeForTest(true)
	defer restore()

	got := confirmRunBody([]string{"select 1", "select 2", "select 3"})
	for _, want := range []string{
		"Execute 3 statements?",
		"1. select 1",
		"2. select 2",
		"3. select 3",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("confirmRunBody = %q, missing %q", got, want)
		}
	}
}

// TestConfirmRunBodyMultiStatementCapsList bounds the rendered list so the
// popup and its hint stay on screen, collapsing the overflow to a tail.
func TestConfirmRunBodyMultiStatementCapsList(t *testing.T) {
	restore := theme.SetMonochromeForTest(true)
	defer restore()

	stmts := make([]string, 20)
	for i := range stmts {
		stmts[i] = "select 1"
	}
	got := confirmRunBody(stmts)
	if strings.Contains(got, " 9. ") {
		t.Fatalf("confirmRunBody listed past the cap:\n%s", got)
	}
	if !strings.Contains(got, "… +12 more") {
		t.Fatalf("confirmRunBody missing overflow tail:\n%s", got)
	}
}

// TestConfirmRunBodySingleStatementHeader verifies a single statement gets
// an action header summarising its verb and effect above the SQL.
func TestConfirmRunBodySingleStatementHeader(t *testing.T) {
	restore := theme.SetMonochromeForTest(true)
	defer restore()

	got := confirmRunBody([]string{"update accounts set active = false"})
	if !strings.Contains(got, "UPDATE · writes data") {
		t.Fatalf("confirmRunBody missing action header:\n%s", got)
	}
	if !strings.Contains(got, "update accounts set active = false") {
		t.Fatalf("confirmRunBody missing statement:\n%s", got)
	}
}
