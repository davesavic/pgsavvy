package controllers

import (
	"context"
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/query"
	"github.com/davesavic/dbsavvy/pkg/session"
)

// TestRelationshipPanelDrillAttachesChildIdentity is the BUG A/B shared
// root-cause regression: drilling into an inbound (child) relationship opens a
// new result tab via OpenResultTab, but the panel never stamped a
// ResultIdentity on it. Without identity, splitBaseTable returns ("","") so the
// panel renders "(no relationships)" on the just-opened child — the iterative
// exploration UX dead-ends after one hop. The reverse SQL is quoted +
// predicated (`SELECT * FROM "public"."orders" WHERE "customer_id"=$1`), which
// DetectFromQuery parses to BaseTable "public.orders".
func TestRelationshipPanelDrillAttachesChildIdentity(t *testing.T) {
	c := testCommon(t)
	ctx, _ := newPanelContext(t)
	tree := &fakePanelTree{}
	// Parent tab: customers(id=42). Use a REAL tabs helper so OpenResultTab
	// creates an inspectable child tab.
	h := newTabWithRow(t, "public.customers", []string{"id"}, []any{int64(42)})
	rev := fkList(models.ForeignKey{
		Schema: "public", Table: "orders",
		Columns:    []string{"customer_id"},
		RefColumns: []string{"id"},
	})
	ctrl := NewRelationshipPanelController(c, CoreDeps{}, ctx, tree, h, fkList(), rev, nil)
	// Reverse-open through the REAL helper (it satisfies the tabs manager +
	// ResultTabIdentityAttacher). runner returns a nil RunHandle, which
	// OpenResultTab accepts (no stream).
	runner := &fakeReverseRunner{}
	ctrl.SetReverseOpen(runner, h, &fakeReverseJumps{})

	if err := registerAndDispatch(t, ctrl, "relationship_panel.toggle"); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	if err := registerAndDispatch(t, ctrl, "relationship_panel.enter"); err != nil {
		t.Fatalf("enter: %v", err)
	}
	if err := registerAndDispatch(t, ctrl, "relationship_panel.enter"); err != nil {
		t.Fatalf("enter (drill): %v", err)
	}

	child := h.Active()
	if child == nil {
		t.Fatal("no active tab after drill")
	}
	_, ri := child.Identity()
	want := query.DetectFromQuery(`SELECT * FROM "public"."orders" WHERE "customer_id"=$1`)
	if ri.BaseTable != want.BaseTable {
		t.Fatalf("child tab BaseTable = %q, want %q", ri.BaseTable, want.BaseTable)
	}
	if ri.BaseTable != "public.orders" {
		t.Fatalf("child tab BaseTable = %q, want %q", ri.BaseTable, "public.orders")
	}
	if !ri.HasRowIdentity {
		t.Fatal("child tab HasRowIdentity = false, want true (single-table SELECT)")
	}
}

// TestRelationshipPanelRefreshesOnTabActivation is the BUG A repaint
// regression: when a new tab is opened/activated while the panel is open, the
// panel must repaint immediately for the new active tab WITHOUT a cursor nudge.
// Here a second tab (orders) is opened on a real helper; the panel's
// activation-refresh hook must rebuild the body against the new active tab so
// the outbound line for orders -> customers appears.
func TestRelationshipPanelRefreshesOnTabActivation(t *testing.T) {
	c := testCommon(t)
	ctx, _ := newPanelContext(t)
	tree := &fakePanelTree{}
	// Parent tab: customers (no outbound FKs).
	h := newTabWithRow(t, "public.customers", []string{"id"}, []any{int64(42)})

	// forwardFK: orders -> customers exists; customers has none.
	fwd := func(_ context.Context, _, table string) ([]models.ForeignKey, error) {
		if table == "orders" {
			return []models.ForeignKey{{
				RefTable: "customers", Columns: []string{"customer_id"},
			}}, nil
		}
		return nil, nil
	}
	ctrl := NewRelationshipPanelController(c, CoreDeps{}, ctx, tree, h, fwd, fkList(), nil)

	if err := registerAndDispatch(t, ctrl, "relationship_panel.toggle"); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	// Customers has no outbound FK: the panel reads "(no relationships)".
	if body := ctx.Body(); strings.Contains(body, "-> customers") {
		t.Fatalf("unexpected outbound on customers tab; got:\n%s", body)
	}

	// Open a NEW tab (orders) and stamp its identity, mirroring the editor run
	// path. Activation must repaint the panel for the new tab.
	if err := h.OpenResultTab("orders", nil); err != nil {
		t.Fatalf("OpenResultTab: %v", err)
	}
	og := h.Active().Grid()
	og.SetColumns([]models.ColumnMeta{{Name: "id"}, {Name: "customer_id"}})
	og.AppendRows([]models.Row{{Values: []any{int64(1), int64(42)}}})
	h.Active().SetIdentity("conn1", query.ResultIdentity{BaseTable: "public.orders", HasRowIdentity: true})

	// Drive the activation-refresh hook the orchestrator wires.
	ctrl.NotifyActiveTabChanged()

	if body := ctx.Body(); !strings.Contains(body, "-> customers") {
		t.Fatalf("panel did not repaint for newly activated orders tab; got:\n%s", body)
	}
}

// TestRelationshipPanelDrillRepaintsChildBody is the end-to-end Bug A/B
// symptom: after drilling into a child the panel must repaint against the child
// tab — deriving its base table from the freshly-attached identity — instead of
// going blank. With the child tab carrying a populated grid + outbound FK
// (orders -> customers), the body must show the orders outbound line, NOT the
// parent customers' relationships.
func TestRelationshipPanelDrillRepaintsChildBody(t *testing.T) {
	c := testCommon(t)
	ctx, _ := newPanelContext(t)
	tree := &fakePanelTree{}
	h := newTabWithRow(t, "public.customers", []string{"id"}, []any{int64(42)})

	// customers: one inbound (orders). orders: one outbound (-> customers).
	fwd := func(_ context.Context, _, table string) ([]models.ForeignKey, error) {
		if table == "orders" {
			return []models.ForeignKey{{RefTable: "customers", Columns: []string{"customer_id"}}}, nil
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

	// childOpener populates the child tab's grid the way a real stream would,
	// so rowSnapshot can read the orders row after the drill.
	tabs := &gridPopulatingTabs{h: h, cols: []string{"id", "customer_id"}, row: []any{int64(7), int64(42)}}
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

	body := ctx.Body()
	if !strings.Contains(body, "-> customers") {
		t.Fatalf("panel did not repaint for drilled-into orders child; got:\n%s", body)
	}
}

// gridPopulatingTabs is a tabs manager that opens the child tab on the real
// helper AND populates its grid + identity, mirroring a stream landing. It
// satisfies FKReverseTabsManager + ResultTabIdentityAttacher by delegating to
// the embedded helper.
type gridPopulatingTabs struct {
	h    *ui.ResultTabsHelper
	cols []string
	row  []any
}

func (g *gridPopulatingTabs) OpenResultTab(label string, rh *session.RunHandle) error {
	if err := g.h.OpenResultTab(label, rh); err != nil {
		return err
	}
	grid := g.h.Active().Grid()
	meta := make([]models.ColumnMeta, len(g.cols))
	for i, c := range g.cols {
		meta[i] = models.ColumnMeta{Name: c}
	}
	grid.SetColumns(meta)
	grid.AppendRows([]models.Row{{Values: g.row}})
	return nil
}

func (g *gridPopulatingTabs) AttachActiveTabIdentity(connID string, ri query.ResultIdentity) {
	g.h.AttachActiveTabIdentity(connID, ri)
}
