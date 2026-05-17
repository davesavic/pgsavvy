package orchestrator_test

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

func TestRunLayoutGatesLimitOverlay(t *testing.T) {
	g, rec := buildTestGui(t)
	// Below threshold → only the LIMIT view is laid out.
	if err := g.RunLayout(5, 5); err != nil {
		t.Fatalf("RunLayout small: %v", err)
	}
	if !rec.HasSetView(string(types.LIMIT)) {
		t.Fatal("expected LIMIT view to be created at 5x5")
	}
	for _, name := range []string{
		string(types.CONNECTIONS),
		string(types.SCHEMAS),
		string(types.TABLES),
	} {
		if rec.HasSetView(name) {
			t.Errorf("did not expect SetView(%q) on tiny terminal", name)
		}
	}
}

func TestRunLayoutSkipsStubContexts(t *testing.T) {
	g, rec := buildTestGui(t)
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout large: %v", err)
	}
	for _, name := range []string{
		string(types.QUERY_EDITOR),
		string(types.TABLE_DATA_EDITOR),
		string(types.RESULT_GRID),
		string(types.PLAN),
		string(types.WHICH_KEY),
		string(types.HISTORY),
	} {
		if rec.HasSetView(name) {
			t.Errorf("stub context %q must not be laid out", name)
		}
	}
}

func TestRunLayoutCreatesSideRails(t *testing.T) {
	g, rec := buildTestGui(t)
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	for _, name := range []string{
		string(types.CONNECTIONS),
		string(types.SCHEMAS),
		string(types.TABLES),
		string(types.COLUMNS),
		string(types.INDEXES),
	} {
		if !rec.HasSetView(name) {
			t.Errorf("side rail %q not laid out at 120x40", name)
		}
	}
}
