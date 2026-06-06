package controllers

import (
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// TestBuildCheatsheetTabsHumanLabels asserts the tab bar shows readable
// labels rather than raw snake_case context slugs (dbsavvy-quyg). The
// rendered Body() carries the header row, so we assert on its contents.
func TestBuildCheatsheetTabsHumanLabels(t *testing.T) {
	render := func(scope types.ContextKey) string { return "body:" + string(scope) }

	popup := BuildCheatsheetTabs(types.QUERY_EDITOR, render)
	header, _, _ := strings.Cut(popup.Body(), "\n")

	if strings.Contains(header, "query_editor") {
		t.Errorf("tab header still shows raw slug: %q", header)
	}
	if !strings.Contains(header, "Query Editor") {
		t.Errorf("tab header missing humanized focused label %q: %q", "Query Editor", header)
	}
	if !strings.Contains(header, "Global") {
		t.Errorf("tab header missing humanized global label %q: %q", "Global", header)
	}
}
