package orchestrator

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/i18n"
)

// TestRailEmptyText_ReturnsContextualPlaceholderPerRail proves the production
// RailEmptyText hook (wired into ctxDeps in wireWithDriver) maps each side
// rail to its contextual i18n placeholder, so the SCHEMAS/TABLES/COLUMNS/
// INDEXES rails actually render text when empty (dbsavvy-fow.5 U7). The
// context-layer consumption is covered by side_rail_empty_state_test.go; this
// closes the wiring gap between the hook and the TranslationSet.
func TestRailEmptyText_ReturnsContextualPlaceholderPerRail(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	hook := railEmptyText(tr)

	cases := map[types.ContextKey]string{
		types.SCHEMAS: tr.EmptySchemasHint,
		types.TABLES:  tr.EmptyTablesHint,
		types.COLUMNS: tr.EmptyColumnsHint,
		types.INDEXES: tr.EmptyIndexesHint,
	}
	for rail, want := range cases {
		if want == "" {
			t.Fatalf("English placeholder for %s is empty; expected non-empty", rail)
		}
		if got := hook(rail); got != want {
			t.Errorf("railEmptyText(%s) = %q, want %q", rail, got, want)
		}
	}

	// A non-rail key (and a nil TranslationSet) must fall through to "" so the
	// rail keeps its prior blank render rather than panicking.
	if got := hook(types.GLOBAL); got != "" {
		t.Errorf("railEmptyText(GLOBAL) = %q, want \"\" (non-rail key)", got)
	}
	if got := railEmptyText(nil)(types.SCHEMAS); got != "" {
		t.Errorf("railEmptyText(nil)(SCHEMAS) = %q, want \"\"", got)
	}
}
