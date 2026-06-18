package context

import (
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/editor/highlight"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/theme"
)

// TestFormatSavedQueryRow_NameBold pins the name/SQL visual distinction: the
// name is emphasised (bold) so it reads as the row label, separated from the
// SQL preview. Run in monochrome so highlight.Highlight passes the SQL through
// verbatim and the row is byte-stable. The bold IS still emitted under
// monochrome — it is the only name/SQL distinction left when colour is off.
func TestFormatSavedQueryRow_NameBold(t *testing.T) {
	defer theme.SetMonochromeForTest(true)()

	got := formatSavedQueryRow(models.SavedQuery{Name: "users", SQL: "select 1"})
	want := "\x1b[1musers\x1b[0m  select 1"
	if got != want {
		t.Fatalf("formatSavedQueryRow\n got: %q\nwant: %q", got, want)
	}
}

// TestFormatSavedQueryRow_SQLHighlighted confirms the SQL preview is run
// through the syntax highlighter in colour mode, so the query is visually
// distinct from the plain-but-bold name.
func TestFormatSavedQueryRow_SQLHighlighted(t *testing.T) {
	defer theme.SetMonochromeForTest(false)()

	got := formatSavedQueryRow(models.SavedQuery{Name: "users", SQL: "select 1"})
	wantSQL := highlight.Highlight("select 1")
	if !strings.Contains(got, wantSQL) {
		t.Fatalf("row missing highlighted SQL\n got: %q\nwant substr: %q", got, wantSQL)
	}
	if !strings.HasPrefix(got, "\x1b[1musers\x1b[0m  ") {
		t.Fatalf("row missing bold name prefix\n got: %q", got)
	}
}
