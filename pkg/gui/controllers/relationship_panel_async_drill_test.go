package controllers

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/query"
	"github.com/davesavic/dbsavvy/pkg/session"
)

// asyncChildTabs models the PRODUCTION reverse-open timing: OpenResultTab opens
// the child tab on the real helper with an EMPTY grid (the stream drains rows
// asynchronously, AFTER openInbound returns). The grid is populated later via
// landRows, mirroring the first batch arriving on the worker/UI thread. Unlike
// gridPopulatingTabs (which populates synchronously inside OpenResultTab and so
// masks the race), this fake reproduces the gap where the activation repaint
// reads an empty row.
type asyncChildTabs struct {
	h    *ui.ResultTabsHelper
	cols []string
	row  []any
}

func (g *asyncChildTabs) OpenResultTab(label string, rh *session.RunHandle) error {
	if err := g.h.OpenResultTab(label, rh); err != nil {
		return err
	}
	// Attach columns only — NO rows yet (the stream has not delivered any).
	grid := g.h.Active().Grid()
	meta := make([]models.ColumnMeta, len(g.cols))
	for i, c := range g.cols {
		meta[i] = models.ColumnMeta{Name: c}
	}
	grid.SetColumns(meta)
	return nil
}

func (g *asyncChildTabs) AttachActiveTabIdentity(connID string, ri query.ResultIdentity) {
	g.h.AttachActiveTabIdentity(connID, ri)
}

// landRows appends the first row batch to the child tab's grid, mirroring the
// stream's initial fill landing after the drill.
func (g *asyncChildTabs) landRows() {
	g.h.Active().Grid().AppendRows([]models.Row{{Values: g.row}})
}

// TestRelationshipPanelSeedsFKValuesWhenChildRowsLandAfterDrill is the GAP
// regression: after drilling into a child whose grid is still empty at
// activation, the outbound line renders the raw "?" fallback and the preview
// never resolves — until the user nudges the cursor. When the first rows LAND
// (the stream's initial fill), the panel must seed the focused row's FK values
// and resolve the preview WITHOUT a manual cursor nudge.
func TestRelationshipPanelSeedsFKValuesWhenChildRowsLandAfterDrill(t *testing.T) {
	c := testCommon(t)
	ctx, _ := newPanelContext(t)
	tree := &fakePanelTree{}
	// Parent tab: customers(id=42), one inbound (orders).
	h := newTabWithRow(t, "public.customers", []string{"id"}, []any{int64(42)})

	// customers: one inbound (orders). orders: one outbound (-> customers).
	fwd := func(_ context.Context, _, table string) ([]models.ForeignKey, error) {
		if table == "orders" {
			return []models.ForeignKey{{
				RefSchema: "public", RefTable: "customers",
				Columns: []string{"customer_id"}, RefColumns: []string{"id"},
			}}, nil
		}
		return nil, nil
	}
	rev := func(_ context.Context, _, table string) ([]models.ForeignKey, error) {
		if table == "customers" {
			return []models.ForeignKey{{
				Schema: "public", Table: "orders",
				Columns: []string{"customer_id"}, RefColumns: []string{"id"},
			}}, nil
		}
		return nil, nil
	}
	ctrl := NewRelationshipPanelController(c, CoreDeps{}, ctx, tree, h, fwd, rev, nil)

	// Outbound preview resolves orders -> customers to a human-readable name.
	ctrl.SetPreviewResolver(previewFn("Alice", nil)) // synchronous (no onWorker)

	// Wire the per-tab grid cursor-change hook exactly as the orchestrator does
	// so a rows-landed notification drives the debounced settle.
	h.SetOnGridCursorChange(ctrl.NotifyCursorChange)

	tabs := &asyncChildTabs{h: h, cols: []string{"id", "customer_id"}, row: []any{int64(7), int64(42)}}
	ctrl.SetReverseOpen(&fakeReverseRunner{}, tabs, &fakeReverseJumps{})

	if err := registerAndDispatch(t, ctrl, "relationship_panel.toggle"); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	if err := registerAndDispatch(t, ctrl, "relationship_panel.enter"); err != nil {
		t.Fatalf("enter: %v", err)
	}
	if err := registerAndDispatch(t, ctrl, "relationship_panel.enter"); err != nil {
		t.Fatalf("enter (drill): %v", err)
	}

	// At this point the child grid is still empty: the activation repaint read a
	// "?" fallback. Now the first batch lands (initial-fill stream).
	tabs.landRows()

	// The rows-landed settle is debounced (NotifyCursorChange arms a 200ms timer
	// that repaints synchronously when no UI hook is wired). Wait past it.
	deadline := time.Now().Add(2 * time.Second)
	var body string
	for time.Now().Before(deadline) {
		body = ctx.Body()
		if strings.Contains(body, "-> customers: Alice") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if strings.Contains(body, "customer_id=?") {
		t.Fatalf("outbound still renders the raw '?' fallback after rows landed; got:\n%s", body)
	}
	if !strings.Contains(body, "-> customers: Alice") {
		t.Fatalf("preview did not resolve after rows landed (no cursor nudge); got:\n%s", body)
	}
}
