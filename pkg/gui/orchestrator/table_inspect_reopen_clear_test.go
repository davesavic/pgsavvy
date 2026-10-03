package orchestrator_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// TestTableInspectReopen_FailedFKRefetchClearsStaleRows is the FM2
// clear-on-reopen invariant: after a successful FK load on table_a, re-opening
// inspect on table_b whose FK fetch ERRORS must NOT leave table_a's foreign
// keys on screen. registerTableInspectOpen clears all four leaves (items AND
// error flags) before spawning the refetch workers, so the failing populate
// pins table_b's error line over a blank leaf — never table_a's stale rows.
func TestTableInspectReopen_FailedFKRefetchClearsStaleRows(t *testing.T) {
	g, drv := buildTestGuiWithHistory(t)
	// Register the shared inspect view so the FK leaf's HandleRender SetContent
	// lands in a buffer the test can read back.
	_, _ = drv.SetView("table_inspect", 0, 0, 80, 24, 0)

	// Conn FK fetches always error (the fake session caches the conn's error
	// fields at AcquireSession time, so a per-table toggle isn't possible; the
	// stale table_a state is seeded directly onto the leaf below).
	driverName, conn := registerWireFake(t, drivers.Capabilities{})
	conn.outboundFKErr = errors.New("boom")
	conn.inboundFKErr = errors.New("boom")

	profile := &models.Connection{Name: "inspect", Driver: driverName, DSN: "postgres://stub"}
	if err := g.HelperBagForTest().Connect.Connect(context.Background(), profile); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	tblB := &models.Table{Schema: "public", Name: "table_b"}
	g.Registry().Tables.SetItems([]any{tblB})
	g.Registry().Tables.SetCursor(0)

	// Simulate a prior successful open on table_a: stale outbound FK on the
	// registry leaf, no error flagged.
	fkLeaf := g.Registry().ForeignKeys
	fkLeaf.SetForeignKeys(
		[]models.ForeignKey{{
			Columns: []string{"author_id"}, RefSchema: "public", RefTable: "users",
			RefColumns: []string{"id"},
		}},
		nil,
	)

	// Re-open on table_b; its FK fetch errors.
	fireTableInspectOpen(t, g)
	g.WaitForWorkersForTest()

	if err := fkLeaf.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.GetViewBuffer("table_inspect")

	if strings.Contains(body, "author_id") {
		t.Errorf("table_a's stale foreign keys leaked after a failed reopen refetch: %q", body)
	}
	if !strings.Contains(body, "could not load outbound foreign keys") {
		t.Errorf("table_b's outbound error line must be pinned after the failed refetch: %q", body)
	}
}

func TestReview_inspector_connection_replacement(t *testing.T) {
	g, d, _ := reviewInspectorGui(t, nil, nil)
	a := &models.Table{Schema: "public", Name: "same"}
	reviewInspectorSelect(g, a)
	fireTableInspectOpen(t, g)
	g.WaitForWorkersForTest()
	old := reviewInspectorCallbacks(d)
	name, conn := registerWireFake(t, drivers.Capabilities{})
	conn.columns = []models.Column{{Name: "replacement"}}
	conn.indexes = []models.Index{{Name: "replacement"}}
	conn.outboundFKs = []models.ForeignKey{{Columns: []string{"replacement"}, RefTable: "replacement"}}
	conn.inboundFKs = conn.outboundFKs
	conn.constraints = []models.Constraint{{Name: "replacement", Kind: "CHECK", Definition: "replacement"}}
	conn.statsRows, conn.statsBytes = 999, 999
	if err := g.HelperBagForTest().Connect.Connect(context.Background(), &models.Connection{Name: name, Driver: name}); err != nil {
		t.Fatal(err)
	}
	g.WaitForWorkersForTest()
	d.drainPending()
	b := &models.Table{Schema: "public", Name: "same"}
	reviewInspectorSelect(g, b)
	fireTableInspectOpen(t, g)
	g.WaitForWorkersForTest()
	d.drainPending()
	reviewInspectorRun(t, old)
	cols, indexes := g.Registry().Columns.Items(), g.Registry().Indexes.Items()
	if len(cols) != 1 || cols[0].(models.Column).Name != "replacement" || len(indexes) != 1 || indexes[0].(models.Index).Name != "replacement" {
		t.Errorf("old connection replaced current leaves: %v / %v", cols, indexes)
	}
	for leaf, body := range map[string]string{"fk": g.Registry().ForeignKeys.BodyText(), "constraint": g.Registry().Constraints.BodyText()} {
		if !strings.Contains(body, "replacement") || strings.Contains(body, "a_same") {
			t.Errorf("old connection replaced %s: %q", leaf, body)
		}
	}
	if a.EstimatedRows.Load() != 0 || b.EstimatedRows.Load() != 999 || g.Registry().TableInspect.IsLoading() {
		t.Errorf("connection ownership/stats lost: A=%d B=%d loading=%t", a.EstimatedRows.Load(), b.EstimatedRows.Load(), g.Registry().TableInspect.IsLoading())
	}
}

func TestReview_inspector_closed_callbacks_rejected(t *testing.T) {
	g, d, _ := reviewInspectorGui(t, nil, nil)
	tbl := &models.Table{Schema: "public", Name: "a"}
	reviewInspectorSelect(g, tbl)
	fireTableInspectOpen(t, g)
	g.WaitForWorkersForTest()
	old := reviewInspectorCallbacks(d)
	if err := g.ContextTree().Pop(); err != nil {
		t.Fatal(err)
	}
	reviewInspectorRun(t, old)
	if len(g.Registry().Columns.Items()) != 0 || len(g.Registry().Indexes.Items()) != 0 || strings.Contains(g.Registry().ForeignKeys.BodyText(), "a_a") || strings.Contains(g.Registry().Constraints.BodyText(), "a_a") || tbl.EstimatedRows.Load() != 0 {
		t.Fatal("closed inspector accepted queued metadata")
	}
}
