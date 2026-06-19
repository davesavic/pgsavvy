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
