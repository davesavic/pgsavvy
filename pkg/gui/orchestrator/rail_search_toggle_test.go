package orchestrator

// Internal test (package orchestrator) so it can reach the unexported
// schemasPickerAdapter. Verifies dbsavvy-ioaj note 3: a show-hidden
// toggle drops any active rail search so n/N cannot later park the
// cursor on a now-hidden row.

import (
	"testing"

	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

func newSchemasCtxForToggle() *guicontext.SchemasContext {
	base := guicontext.NewBaseContext(guicontext.BaseContextOpts{
		Key:      types.SCHEMAS,
		ViewName: string(types.SCHEMAS),
		Kind:     types.SIDE_CONTEXT,
	})
	// Empty Deps: HandleRender is nil-safe (GuiDriver guards in writeView /
	// scrollSideRailIntoView).
	c := guicontext.NewSchemasContext(base, types.ContextTreeDeps{})
	c.SetItems([]any{
		models.Schema{Name: "public"},
		models.Schema{Name: "audit"},
		models.Schema{Name: "audit_log"},
	})
	return c
}

// AC: with a search active on SCHEMAS, the show-hidden toggle path
// (schemasPickerAdapter.ToggleShowHidden) clears the search.
func TestSchemasToggleShowHiddenClearsActiveSearch(t *testing.T) {
	ctx := newSchemasCtxForToggle()
	ctx.SetSearch("audit")
	if !ctx.SearchActive() {
		t.Fatal("precondition: search not active after SetSearch")
	}

	schemasPickerAdapter{registry: ctx}.ToggleShowHidden()

	if ctx.SearchActive() {
		t.Fatal("SearchActive = true after ToggleShowHidden; want cleared (note 3)")
	}
	if got := len(ctx.Matches()); got != 0 {
		t.Fatalf("Matches = %d after toggle; want 0 (no stale highlight)", got)
	}
}

// AC: the toggle still flips the show-hidden mode (clear-on-toggle does
// not swallow the toggle's primary effect).
func TestSchemasToggleShowHiddenStillToggles(t *testing.T) {
	ctx := newSchemasCtxForToggle()
	before := ctx.GetShowHiddenMode()

	schemasPickerAdapter{registry: ctx}.ToggleShowHidden()

	if ctx.GetShowHiddenMode() == before {
		t.Fatal("ToggleShowHidden did not flip show-hidden mode")
	}
}
