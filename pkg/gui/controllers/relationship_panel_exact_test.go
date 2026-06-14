package controllers

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// exactFn builds a static exact-count resolver returning the same value/error
// for every inbound FK.
func exactFn(val int64, err error) relationshipExactLookup {
	return func(_ context.Context, _ models.ForeignKey, _ []any) (int64, error) {
		return val, err
	}
}

// inboundOnlyTabAndRev builds a customers-row tab with exactly one inbound FK
// (orders.customer_id -> customers.id) and no outbound, so the panel selection
// seeds directly on the inbound line when focused.
func inboundOnlyTabAndRev(t *testing.T, id int64) (*RelationshipPanelController, *fakePanelTree) {
	t.Helper()
	c := testCommon(t)
	ctx, _ := newPanelContext(t)
	tree := &fakePanelTree{}
	h := newTabWithRow(t, "public.customers", []string{"id"}, []any{id})
	rev := fkList(models.ForeignKey{
		Schema: "public", Table: "orders",
		Columns:    []string{"customer_id"},
		RefColumns: []string{"id"},
	})
	ctrl := NewRelationshipPanelController(c, CoreDeps{}, ctx, tree, h, fkList(), rev, nil)
	return ctrl, tree
}

// focusInbound opens the panel and enters it so the single inbound line is
// selected, driving maybeStartExactFill synchronously (no onWorker wired).
func focusInbound(t *testing.T, ctrl *RelationshipPanelController) {
	t.Helper()
	if err := registerAndDispatch(t, ctrl, "relationship_panel.toggle"); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	if err := registerAndDispatch(t, ctrl, "relationship_panel.enter"); err != nil {
		t.Fatalf("enter: %v", err)
	}
}

// TestRelationshipPanelExactCountReplacesEstimate focuses an inbound line and
// confirms the exact COUNT(*) replaces the ~estimate ("<- orders 1187", no ~).
func TestRelationshipPanelExactCountReplacesEstimate(t *testing.T) {
	ctrl, _ := inboundOnlyTabAndRev(t, 42)
	ctrl.SetEstimateResolver(estimateFn(1200, nil))
	ctrl.SetExactResolver(exactFn(1187, nil))

	focusInbound(t, ctrl)

	body := ctrl.renderBodyFromState()
	if !strings.Contains(body, "<- orders 1187") {
		t.Fatalf("exact count line missing; got:\n%s", body)
	}
	if strings.Contains(body, "~1.2k") {
		t.Fatalf("estimate should be replaced by exact; got:\n%s", body)
	}
}

// TestRelationshipPanelExactCountZero renders an exact "0" (boundary).
func TestRelationshipPanelExactCountZero(t *testing.T) {
	ctrl, _ := inboundOnlyTabAndRev(t, 7)
	ctrl.SetEstimateResolver(estimateFn(5, nil))
	ctrl.SetExactResolver(exactFn(0, nil))

	focusInbound(t, ctrl)

	body := ctrl.renderBodyFromState()
	if !strings.Contains(body, "<- orders 0") {
		t.Fatalf("exact zero count missing; got:\n%s", body)
	}
}

// TestRelationshipPanelExactCountTimeoutKeepsEstimate confirms a DeadlineExceeded
// from the exact resolver leaves the ~estimate intact (no error marker, no hang).
func TestRelationshipPanelExactCountTimeoutKeepsEstimate(t *testing.T) {
	ctrl, _ := inboundOnlyTabAndRev(t, 42)
	ctrl.SetEstimateResolver(estimateFn(1200, nil))
	ctrl.SetExactResolver(exactFn(0, context.DeadlineExceeded))

	focusInbound(t, ctrl)

	body := ctrl.renderBodyFromState()
	if !strings.Contains(body, "<- orders ~1.2k") {
		t.Fatalf("timeout should keep the estimate; got:\n%s", body)
	}
}

// TestRelationshipPanelExactCountErrorMarker confirms a non-timeout error renders
// the muted exact-error marker while the estimate fields survive.
func TestRelationshipPanelExactCountErrorMarker(t *testing.T) {
	ctrl, _ := inboundOnlyTabAndRev(t, 42)
	ctrl.SetEstimateResolver(estimateFn(1200, nil))
	ctrl.SetExactResolver(exactFn(0, context.Canceled)) // non-timeout error

	focusInbound(t, ctrl)

	body := ctrl.renderBodyFromState()
	if !strings.Contains(body, "<- orders !?") {
		t.Fatalf("exact-error marker missing; got:\n%s", body)
	}
}

// TestRelationshipPanelExactCountCachedOnRefocus confirms refocusing the same
// inbound line on the same row issues no new exact query (cache hit).
func TestRelationshipPanelExactCountCachedOnRefocus(t *testing.T) {
	ctrl, _ := inboundOnlyTabAndRev(t, 42)
	ctrl.SetEstimateResolver(estimateFn(1200, nil))
	var calls int32
	ctrl.SetExactResolver(func(_ context.Context, _ models.ForeignKey, _ []any) (int64, error) {
		atomic.AddInt32(&calls, 1)
		return 1187, nil
	})

	focusInbound(t, ctrl) // first focus -> one exact query
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("exact ran %d times on first focus; want 1", n)
	}
	// Exit + re-enter (same row, same relationship): cache hit, no new query.
	if err := registerAndDispatch(t, ctrl, "relationship_panel.exit"); err != nil {
		t.Fatalf("exit: %v", err)
	}
	if err := registerAndDispatch(t, ctrl, "relationship_panel.enter"); err != nil {
		t.Fatalf("re-enter: %v", err)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("exact ran %d times after refocus; want 1 (cached)", n)
	}
}

// TestRelationshipPanelExactCountSupersededByEpoch confirms a stale exact fill
// (older epoch) does not run after the row changed.
func TestRelationshipPanelExactCountSupersededByEpoch(t *testing.T) {
	ctrl, _ := inboundOnlyTabAndRev(t, 42)
	ctrl.SetEstimateResolver(estimateFn(1200, nil))
	var calls int32
	ctrl.SetExactResolver(func(_ context.Context, _ models.ForeignKey, _ []any) (int64, error) {
		atomic.AddInt32(&calls, 1)
		return 1187, nil
	})

	// Open + enter so a relationship is selected and the panel is focused.
	if err := registerAndDispatch(t, ctrl, "relationship_panel.toggle"); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	if err := registerAndDispatch(t, ctrl, "relationship_panel.enter"); err != nil {
		t.Fatalf("enter: %v", err)
	}
	ctrl.ClearCaches()
	atomic.StoreInt32(&calls, 0)
	// Advance the epoch past the one passed to maybeStartExactFill: the fill must
	// drop before issuing the count (row changed during fill).
	stale := ctrl.currentEpoch()
	ctrl.epoch.Add(1)
	ctrl.maybeStartExactFill(stale)
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Fatalf("exact ran %d times for a superseded epoch; want 0", n)
	}
}

// TestRelationshipPanelExactCountNotFiredWhenUnfocused confirms the exact count
// does NOT run on settle (follow mode) — only when an inbound line is focused.
func TestRelationshipPanelExactCountNotFiredWhenUnfocused(t *testing.T) {
	ctrl, _ := inboundOnlyTabAndRev(t, 42)
	ctrl.SetEstimateResolver(estimateFn(1200, nil))
	var calls int32
	ctrl.SetExactResolver(func(_ context.Context, _ models.ForeignKey, _ []any) (int64, error) {
		atomic.AddInt32(&calls, 1)
		return 1187, nil
	})

	// Toggle open only (no enter): follow mode, not focused.
	if err := registerAndDispatch(t, ctrl, "relationship_panel.toggle"); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Fatalf("exact ran %d times in follow mode; want 0 (focus-only)", n)
	}
	body := ctrl.renderBodyFromState()
	if !strings.Contains(body, "<- orders ~1.2k") {
		t.Fatalf("unfocused panel should show the estimate; got:\n%s", body)
	}
}

// TestRelationshipPanelEvictExactCountsPerTable confirms per-table eviction drops
// the cached count for the matching child table so a re-focus recomputes.
func TestRelationshipPanelEvictExactCountsPerTable(t *testing.T) {
	ctrl, _ := inboundOnlyTabAndRev(t, 42)
	ctrl.SetEstimateResolver(estimateFn(1200, nil))
	var calls int32
	ctrl.SetExactResolver(func(_ context.Context, _ models.ForeignKey, _ []any) (int64, error) {
		atomic.AddInt32(&calls, 1)
		return 1187, nil
	})

	focusInbound(t, ctrl)
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("exact ran %d times on first focus; want 1", n)
	}

	// A non-matching table eviction leaves the cache intact.
	ctrl.EvictExactCounts("public", "widgets")
	ctrl.maybeStartExactFill(ctrl.currentEpoch())
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("exact ran %d times after non-matching evict; want 1", n)
	}

	// Evicting the matching child table (orders) forces a recompute on refocus.
	ctrl.EvictExactCounts("public", "orders")
	ctrl.maybeStartExactFill(ctrl.currentEpoch())
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("exact ran %d times after matching evict; want 2 (recomputed)", n)
	}
}

// TestRelationshipPanelEvictAllExactCountsWhenClosed confirms the coarse eviction
// works even when the panel is CLOSED (cache survives close), so a committed-
// then-revisited row recomputes.
func TestRelationshipPanelEvictAllExactCountsWhenClosed(t *testing.T) {
	ctrl, _ := inboundOnlyTabAndRev(t, 42)
	ctrl.SetEstimateResolver(estimateFn(1200, nil))
	var calls int32
	ctrl.SetExactResolver(func(_ context.Context, _ models.ForeignKey, _ []any) (int64, error) {
		atomic.AddInt32(&calls, 1)
		return 1187, nil
	})

	focusInbound(t, ctrl)
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("exact ran %d times on first focus; want 1", n)
	}

	// EvictAllExactCounts is callable on a closed panel (the cache is independent
	// of open state). Note toggle-close also ClearCaches, so call evict directly.
	ctrl.EvictAllExactCounts()
	ctrl.maybeStartExactFill(ctrl.currentEpoch())
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("exact ran %d times after EvictAllExactCounts; want 2 (recomputed)", n)
	}
}
