package context

import (
	"bytes"
	"os"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
)

// Concurrency N/A: every method exercised here runs on the single gocui
// MainLoop (UI thread), so the recorder/driver spies need no synchronization.

// TestRailMarkerParity_BothRailsUseIdenticalActiveMarkerFormat is the
// cross-rail parity guard (coverage item 5). Both the QUERY_RAIL and the
// SCHEMA_RAIL must emit the EXACT same active-tab marker format via SetViewTabs
// — "[Label]" on the active tab, the bare label otherwise — proving the core's
// tabLabels()/markActiveTab is the SINGLE source of the marker. Asserted by
// reading each rail's AllSetViewTabsCalls / recorded tabs calls and comparing
// the marked vs unmarked labels on identical active indices.
func TestRailMarkerParity_BothRailsUseIdenticalActiveMarkerFormat(t *testing.T) {
	// QUERY_RAIL: render on tab 0, switch, render on tab 1.
	qDrv := testfake.NewRecorderGuiDriver()
	qDrv.SetRealView(queryRailViewName, gocui.NewView(queryRailViewName, 0, 0, 20, 10, gocui.OutputNormal))
	qRail, _, _ := newQueryRail(qDrv)
	if err := qRail.HandleRender(); err != nil {
		t.Fatalf("query rail HandleRender frame 1: %v", err)
	}
	qRail.SetActiveTab(1)
	if err := qRail.HandleRender(); err != nil {
		t.Fatalf("query rail HandleRender frame 2: %v", err)
	}
	qCalls := qDrv.AllSetViewTabsCalls()
	if len(qCalls) != 2 {
		t.Fatalf("query rail published %d tab strips, want 2", len(qCalls))
	}

	// SCHEMA_RAIL: render on Schemas, switch to Tables, render again.
	sDrv := &railTestDriver{}
	tree := newRailTree(sDrv)
	if err := tree.SchemaRail.HandleRender(); err != nil {
		t.Fatalf("schema rail HandleRender frame 1: %v", err)
	}
	tree.SchemaRail.SetActiveTab(SchemaRailTabTables)
	if err := tree.SchemaRail.HandleRender(); err != nil {
		t.Fatalf("schema rail HandleRender frame 2: %v", err)
	}
	if len(sDrv.tabsCalls) != 2 {
		t.Fatalf("schema rail published %d tab strips, want 2", len(sDrv.tabsCalls))
	}

	// markActiveTab is the single source: re-deriving the marker from the raw
	// labels must reproduce exactly what each rail published. Compare both
	// rails frame-by-frame against the SAME markActiveTab function.
	assertMarkerFormat := func(rail string, labels []string, active int) {
		t.Helper()
		for i, got := range labels {
			rawWant := stripMarker(got)
			want := rawWant
			if i == active {
				want = markActiveTab(rawWant)
			}
			if got != want {
				t.Errorf("%s: label[%d]=%q (active=%d), want %q — marker format diverged from markActiveTab",
					rail, i, got, active, want)
			}
		}
	}

	// Frame 1: index 0 active on both. Frame 2: index 1 active on both.
	assertMarkerFormat("query frame1", qCalls[0].Labels, 0)
	assertMarkerFormat("query frame2", qCalls[1].Labels, 1)
	assertMarkerFormat("schema frame1", sDrv.tabsCalls[0].labels, SchemaRailTabSchemas)
	assertMarkerFormat("schema frame2", sDrv.tabsCalls[1].labels, SchemaRailTabTables)

	// Cross-rail concrete check: the active marker on each rail is "[" + raw +
	// "]" — e.g. query frame2 "[History]", schema frame2 "[Tables]" — proving
	// both go through the identical core formatter.
	if qCalls[1].Labels[1] != "[History]" {
		t.Errorf("query active marker = %q, want %q", qCalls[1].Labels[1], "[History]")
	}
	if sDrv.tabsCalls[1].labels[SchemaRailTabTables] != "[Tables]" {
		t.Errorf("schema active marker = %q, want %q", sDrv.tabsCalls[1].labels[SchemaRailTabTables], "[Tables]")
	}
}

// stripMarker removes the active-tab bracket wrapper if present, so a published
// label can be reduced to its raw form for re-derivation. Mirrors the inverse
// of markActiveTab ("[" + label + "]").
func stripMarker(label string) string {
	if len(label) >= 2 && label[0] == '[' && label[len(label)-1] == ']' {
		return label[1 : len(label)-1]
	}
	return label
}

// TestTabbedRail_LOCInvariantUnderBaseline is the LOC-invariant guard
// (coverage item 7). The refactor folded QueryRailContext + SchemaRailContext
// into a shared TabbedRailContext core. The pre-refactor baseline was 564 LOC
// (query 330 + schema 234, with no shared core file). The post-refactor total
// of tabbed_rail_context.go + query_rail_context.go + schema_rail_context.go
// MUST stay strictly under that baseline, or the consolidation has regressed.
// Deterministic: counts newlines in the three source files via os.ReadFile.
func TestTabbedRail_LOCInvariantUnderBaseline(t *testing.T) {
	const baseline = 564 // pre-refactor: query 330 + schema 234.
	files := []string{
		"tabbed_rail_context.go",
		"query_rail_context.go",
		"schema_rail_context.go",
	}
	total := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		total += bytes.Count(b, []byte{'\n'})
	}
	if total >= baseline {
		t.Errorf("3-file LOC total = %d, want < %d (refactor must not exceed pre-refactor baseline)",
			total, baseline)
	}
	t.Logf("3-file LOC total = %d (baseline %d)", total, baseline)
}
