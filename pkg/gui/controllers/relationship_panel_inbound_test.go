package controllers

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/query"
)

// newTabWithRowIdentity opens a result tab populated with one row and stamps
// the supplied identity (so HasRowIdentity can be toggled for degrade tests).
func newTabWithRowIdentity(t *testing.T, ri query.ResultIdentity, cols []string, row []any) *ui.ResultTabsHelper {
	t.Helper()
	h := ui.NewResultTabsHelper(ui.ResultTabsHelperDeps{})
	if err := h.OpenResultTab("t", nil); err != nil {
		t.Fatalf("OpenResultTab: %v", err)
	}
	tab := h.Active()
	g := tab.Grid()
	meta := make([]models.ColumnMeta, len(cols))
	for i, c := range cols {
		meta[i] = models.ColumnMeta{Name: c}
	}
	g.SetColumns(meta)
	g.AppendRows([]models.Row{{Values: row}})
	tab.SetIdentity("conn1", ri)
	return h
}

// estimateFn builds a static estimate resolver returning the same value/error
// for every inbound FK.
func estimateFn(val int64, err error) relationshipEstimateLookup {
	return func(_ context.Context, _ models.ForeignKey, _ []any) (int64, error) {
		return val, err
	}
}

func TestHumanizeEstimate(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{-5, "0"},
		{7, "7"},
		{999, "999"},
		{1000, "1k"},
		{1200, "1.2k"},
		{1250, "1.2k"}, // one-decimal place, rounds down-ish to 1.2
		{2000, "2k"},
		{999999, "1000k"},
		{1_000_000, "1M"},
		{1_200_000, "1.2M"},
	}
	for _, c := range cases {
		if got := humanizeEstimate(c.in); got != c.want {
			t.Errorf("humanizeEstimate(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestRelationshipPanelInboundRendersEstimates resolves a per-row planner
// estimate and renders "<- <child> ~<estimate>".
func TestRelationshipPanelInboundRendersEstimates(t *testing.T) {
	c := testCommon(t)
	ctx, _ := newPanelContext(t)
	tree := &fakePanelTree{}
	h := newTabWithRow(t, "public.customers", []string{"id"}, []any{int64(42)})
	rev := fkList(models.ForeignKey{
		Schema: "public", Table: "orders",
		Columns:    []string{"customer_id"},
		RefColumns: []string{"id"},
	})

	ctrl := NewRelationshipPanelController(c, CoreDeps{}, ctx, tree, h, fkList(), rev, nil)
	ctrl.SetEstimateResolver(estimateFn(1200, nil)) // synchronous (no onWorker)

	// Toggle open runs the synchronous estimate fill (no onWorker wired) and
	// requires IsOpen()==true (the fill guards on it).
	if err := registerAndDispatch(t, ctrl, "relationship_panel.toggle"); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	body := ctrl.renderBodyFromState()
	if !strings.Contains(body, "<- orders ~1.2k") {
		t.Fatalf("inbound estimate line missing; got:\n%s", body)
	}
}

// TestRelationshipPanelInboundZeroChildren renders "~0" for a zero estimate.
func TestRelationshipPanelInboundZeroChildren(t *testing.T) {
	c := testCommon(t)
	ctx, _ := newPanelContext(t)
	tree := &fakePanelTree{}
	h := newTabWithRow(t, "public.customers", []string{"id"}, []any{int64(7)})
	rev := fkList(models.ForeignKey{
		Schema: "public", Table: "orders",
		Columns:    []string{"customer_id"},
		RefColumns: []string{"id"},
	})
	ctrl := NewRelationshipPanelController(c, CoreDeps{}, ctx, tree, h, fkList(), rev, nil)
	ctrl.SetEstimateResolver(estimateFn(0, nil))

	if err := registerAndDispatch(t, ctrl, "relationship_panel.toggle"); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	body := ctrl.renderBodyFromState()
	if !strings.Contains(body, "<- orders ~0") {
		t.Fatalf("zero-children estimate missing; got:\n%s", body)
	}
}

// TestRelationshipPanelInboundEstimateErrorMarker degrades a single line on a
// resolver error while the panel survives.
func TestRelationshipPanelInboundEstimateErrorMarker(t *testing.T) {
	c := testCommon(t)
	ctx, _ := newPanelContext(t)
	tree := &fakePanelTree{}
	h := newTabWithRow(t, "public.customers", []string{"id"}, []any{int64(7)})
	rev := fkList(models.ForeignKey{
		Schema: "public", Table: "orders",
		Columns:    []string{"customer_id"},
		RefColumns: []string{"id"},
	})
	ctrl := NewRelationshipPanelController(c, CoreDeps{}, ctx, tree, h, fkList(), rev, nil)
	ctrl.SetEstimateResolver(estimateFn(0, context.DeadlineExceeded))

	if err := registerAndDispatch(t, ctrl, "relationship_panel.toggle"); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	body := ctrl.renderBodyFromState()
	if !strings.Contains(body, "<- orders ~?") {
		t.Fatalf("degraded estimate marker missing; got:\n%s", body)
	}
}

// TestRelationshipPanelInboundDegradesWithoutRowIdentity renders the
// needs-a-PK note and issues ZERO inbound queries (FK lookup + estimate).
func TestRelationshipPanelInboundDegradesWithoutRowIdentity(t *testing.T) {
	c := testCommon(t)
	ctx, _ := newPanelContext(t)
	tree := &fakePanelTree{}
	// No row identity (join/view): HasRowIdentity=false.
	h := newTabWithRowIdentity(t, query.ResultIdentity{BaseTable: "v", HasRowIdentity: false},
		[]string{"id"}, []any{int64(1)})

	var revCalls int32
	rev := func(_ context.Context, _, _ string) ([]models.ForeignKey, error) {
		atomic.AddInt32(&revCalls, 1)
		return []models.ForeignKey{{Table: "orders", Columns: []string{"customer_id"}, RefColumns: []string{"id"}}}, nil
	}
	var estCalls int32
	est := func(_ context.Context, _ models.ForeignKey, _ []any) (int64, error) {
		atomic.AddInt32(&estCalls, 1)
		return 5, nil
	}
	ctrl := NewRelationshipPanelController(c, CoreDeps{}, ctx, tree, h, fkList(), rev, nil)
	ctrl.SetEstimateResolver(est)

	if err := registerAndDispatch(t, ctrl, "relationship_panel.toggle"); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	body := ctrl.renderBodyFromState()

	if !strings.Contains(body, "inbound needs a primary key") {
		t.Fatalf("degrade note missing; got:\n%s", body)
	}
	if n := atomic.LoadInt32(&revCalls); n != 0 {
		t.Fatalf("reverse FK lookup ran %d times without row identity; want 0", n)
	}
	if n := atomic.LoadInt32(&estCalls); n != 0 {
		t.Fatalf("estimate ran %d times without row identity; want 0", n)
	}
}

// TestRelationshipPanelInboundCachedOnRevisit confirms a second fill for the
// same row issues no new estimate query (cache hit).
func TestRelationshipPanelInboundCachedOnRevisit(t *testing.T) {
	c := testCommon(t)
	ctx, _ := newPanelContext(t)
	tree := &fakePanelTree{}
	h := newTabWithRow(t, "public.customers", []string{"id"}, []any{int64(42)})
	rev := fkList(models.ForeignKey{
		Schema: "public", Table: "orders",
		Columns:    []string{"customer_id"},
		RefColumns: []string{"id"},
	})
	var calls int32
	est := func(_ context.Context, _ models.ForeignKey, _ []any) (int64, error) {
		atomic.AddInt32(&calls, 1)
		return 5, nil
	}
	ctrl := NewRelationshipPanelController(c, CoreDeps{}, ctx, tree, h, fkList(), rev, nil)
	ctrl.SetEstimateResolver(est)

	// Toggle open runs the first fill (calls==1).
	if err := registerAndDispatch(t, ctrl, "relationship_panel.toggle"); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	// Rebuild (same row) + fill again: cache hit, no new query.
	_ = ctrl.renderBody()
	ctrl.startEstimateFill(ctrl.currentEpoch())
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("estimate ran %d times; want 1 (cached on revisit)", n)
	}
}

// TestRelationshipPanelInboundSupersededByEpoch confirms a stale fill (older
// epoch) does not run estimates after the row changed.
func TestRelationshipPanelInboundSupersededByEpoch(t *testing.T) {
	c := testCommon(t)
	ctx, _ := newPanelContext(t)
	tree := &fakePanelTree{}
	h := newTabWithRow(t, "public.customers", []string{"id"}, []any{int64(42)})
	rev := fkList(models.ForeignKey{
		Schema: "public", Table: "orders",
		Columns:    []string{"customer_id"},
		RefColumns: []string{"id"},
	})
	var calls int32
	est := func(_ context.Context, _ models.ForeignKey, _ []any) (int64, error) {
		atomic.AddInt32(&calls, 1)
		return 5, nil
	}
	ctrl := NewRelationshipPanelController(c, CoreDeps{}, ctx, tree, h, fkList(), rev, nil)
	ctrl.SetEstimateResolver(est)
	// Open the panel so IsOpen()==true (the fill guards on it). The toggle's
	// own fill resolves the estimate once (calls==1); clear the cache so the
	// supersede check below is unambiguous.
	if err := registerAndDispatch(t, ctrl, "relationship_panel.toggle"); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	ctrl.ClearCaches()
	atomic.StoreInt32(&calls, 0)
	_ = ctrl.renderBody()
	// Advance the epoch past the one we pass to startEstimateFill: the fill must
	// drop before issuing any estimate (row changed during fill).
	stale := ctrl.currentEpoch()
	ctrl.epoch.Add(1)
	ctrl.startEstimateFill(stale)
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Fatalf("estimate ran %d times for a superseded epoch; want 0", n)
	}
}

// TestRelationshipPanelInboundEnterOpensChildTab confirms Enter on a focused
// inbound relationship pushes a jump and opens a child tab via the reverse-open
// surfaces (reusing buildFKReverseSQL).
func TestRelationshipPanelInboundEnterOpensChildTab(t *testing.T) {
	c := testCommon(t)
	ctx, _ := newPanelContext(t)
	tree := &fakePanelTree{}
	h := newTabWithRow(t, "public.customers", []string{"id"}, []any{int64(42)})
	// No outbound, one inbound -> selection 0 lands on the inbound line.
	rev := fkList(models.ForeignKey{
		Schema: "public", Table: "orders",
		Columns:    []string{"customer_id"},
		RefColumns: []string{"id"},
	})
	ctrl := NewRelationshipPanelController(c, CoreDeps{}, ctx, tree, h, fkList(), rev, nil)

	runner := &fakeReverseRunner{}
	tabs := &fakeReverseTabs{}
	jumps := &fakeReverseJumps{}
	ctrl.SetReverseOpen(runner, tabs, jumps)

	// Open + enter the panel (focus), then Enter again to act on the inbound.
	if err := registerAndDispatch(t, ctrl, "relationship_panel.toggle"); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	if err := registerAndDispatch(t, ctrl, "relationship_panel.enter"); err != nil {
		t.Fatalf("enter: %v", err)
	}
	if err := registerAndDispatch(t, ctrl, "relationship_panel.enter"); err != nil {
		t.Fatalf("enter (act): %v", err)
	}

	got := runner.Captured()
	wantSQL := `SELECT * FROM "public"."orders" WHERE "customer_id"=$1`
	if got.SQL != wantSQL {
		t.Fatalf("reverse-open SQL = %q, want %q", got.SQL, wantSQL)
	}
	if len(got.Args) != 1 || got.Args[0] != int64(42) {
		t.Fatalf("reverse-open args = %v, want [42]", got.Args)
	}
	if tabs.calls != 1 {
		t.Fatalf("OpenResultTab calls = %d, want 1", tabs.calls)
	}
	if len(jumps.pushed) != 1 {
		t.Fatalf("jump pushes = %d, want 1 (pushed before open)", len(jumps.pushed))
	}
}

// TestRelationshipPanelClearCachesDropsEstimates confirms ClearCaches drops the
// estimate cache so a subsequent fill re-resolves.
func TestRelationshipPanelClearCachesDropsEstimates(t *testing.T) {
	c := testCommon(t)
	ctx, _ := newPanelContext(t)
	tree := &fakePanelTree{}
	h := newTabWithRow(t, "public.customers", []string{"id"}, []any{int64(42)})
	rev := fkList(models.ForeignKey{
		Schema: "public", Table: "orders",
		Columns:    []string{"customer_id"},
		RefColumns: []string{"id"},
	})
	var calls int32
	est := func(_ context.Context, _ models.ForeignKey, _ []any) (int64, error) {
		atomic.AddInt32(&calls, 1)
		return 5, nil
	}
	ctrl := NewRelationshipPanelController(c, CoreDeps{}, ctx, tree, h, fkList(), rev, nil)
	ctrl.SetEstimateResolver(est)

	if err := registerAndDispatch(t, ctrl, "relationship_panel.toggle"); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	ctrl.ClearCaches()
	_ = ctrl.renderBody()
	ctrl.startEstimateFill(ctrl.currentEpoch())
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("estimate ran %d times; want 2 (cache cleared between fills)", n)
	}
}
