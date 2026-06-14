package controllers

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	helpers "github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/query"
	"github.com/davesavic/dbsavvy/pkg/session"
)

// previewFn builds a static preview resolver returning val/err for any FK.
func previewFn(val any, err error) relationshipPreviewLookup {
	return func(_ context.Context, _ models.ForeignKey, _ []any) (any, error) {
		return val, err
	}
}

// recordingPreview captures the refValues it was called with, for supersede
// assertions.
type recordingPreview struct {
	mu    sync.Mutex
	calls [][]any
	val   any
}

func (r *recordingPreview) fn() relationshipPreviewLookup {
	return func(_ context.Context, _ models.ForeignKey, refValues []any) (any, error) {
		r.mu.Lock()
		r.calls = append(r.calls, append([]any(nil), refValues...))
		r.mu.Unlock()
		return r.val, nil
	}
}

// count returns the number of recorded calls (mutex-guarded — the resolver
// runs on the debounced fill worker goroutine).
func (r *recordingPreview) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// snapshot returns a copy of the recorded calls under the lock.
func (r *recordingPreview) snapshot() [][]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]any(nil), r.calls...)
}

func TestRelationshipPanelOutboundPreview(t *testing.T) {
	c := testCommon(t)
	ctx, _ := newPanelContext(t)
	tree := &fakePanelTree{}
	h := newTabWithRow(t, "public.orders", []string{"id", "customer_id"}, []any{7, 42})
	fwd := fkList(models.ForeignKey{
		RefSchema: "public", RefTable: "customers",
		Columns: []string{"customer_id"}, RefColumns: []string{"id"},
	})

	ctrl := NewRelationshipPanelController(c, CoreDeps{}, ctx, tree, h, fwd, fkList(), nil)
	ctrl.SetPreviewResolver(previewFn("Acme Corp", nil))
	// nil onWorker => synchronous fill.

	if err := registerAndDispatch(t, ctrl, "relationship_panel.toggle"); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	body := ctrl.renderBodyFromState()
	if !strings.Contains(body, "-> customers: Acme Corp") {
		t.Fatalf("expected human-readable preview; got:\n%s", body)
	}
}

func TestRelationshipPanelNullFKRendersNullNotJumpable(t *testing.T) {
	c := testCommon(t)
	ctx, _ := newPanelContext(t)
	tree := &fakePanelTree{}
	// customer_id is NULL.
	h := newTabWithRow(t, "public.orders", []string{"id", "customer_id"}, []any{7, nil})
	fwd := fkList(models.ForeignKey{
		RefSchema: "public", RefTable: "customers",
		Columns: []string{"customer_id"}, RefColumns: []string{"id"},
	})

	ctrl := NewRelationshipPanelController(c, CoreDeps{}, ctx, tree, h, fwd, fkList(), nil)
	// A preview resolver that would panic if called proves the NULL line never
	// issues a query.
	ctrl.SetPreviewResolver(func(context.Context, models.ForeignKey, []any) (any, error) {
		t.Fatal("preview resolver called for a NULL FK")
		return nil, nil
	})

	if err := registerAndDispatch(t, ctrl, "relationship_panel.toggle"); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	body := ctrl.renderBodyFromState()
	if !strings.Contains(body, "-> customers: (null)") {
		t.Fatalf("expected (null) line; got:\n%s", body)
	}
	// Focus + Enter on the NULL relationship must be a no-op (not jumpable).
	if err := registerAndDispatch(t, ctrl, "relationship_panel.enter"); err != nil {
		t.Fatalf("enter: %v", err)
	}
	if err := registerAndDispatch(t, ctrl, "relationship_panel.enter"); err != nil {
		t.Fatalf("enter(jump) on null: %v", err)
	}
}

func TestRelationshipPanelPreviewErrorKeepsRawFallback(t *testing.T) {
	c := testCommon(t)
	ctx, _ := newPanelContext(t)
	tree := &fakePanelTree{}
	h := newTabWithRow(t, "public.orders", []string{"id", "customer_id"}, []any{7, 42})
	fwd := fkList(models.ForeignKey{
		RefSchema: "public", RefTable: "customers",
		Columns: []string{"customer_id"}, RefColumns: []string{"id"},
	})

	ctrl := NewRelationshipPanelController(c, CoreDeps{}, ctx, tree, h, fwd, fkList(), nil)
	ctrl.SetPreviewResolver(previewFn(nil, errors.New("lookup timeout")))

	if err := registerAndDispatch(t, ctrl, "relationship_panel.toggle"); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	body := ctrl.renderBodyFromState()
	// Error => keep the raw "<col>=<val>" fallback; panel alive.
	if !strings.Contains(body, "-> customers (customer_id=42)") {
		t.Fatalf("expected raw fallback line on preview error; got:\n%s", body)
	}
}

func TestRelationshipPanelCompositeMismatchNoQuery(t *testing.T) {
	c := testCommon(t)
	ctx, _ := newPanelContext(t)
	tree := &fakePanelTree{}
	h := newTabWithRow(t, "public.t", []string{"a", "b"}, []any{1, 2})
	// Columns has 2 entries but RefColumns only 1 => mismatch; must be refused
	// (no preview query, raw fallback).
	fwd := fkList(models.ForeignKey{
		RefSchema: "public", RefTable: "parent",
		Columns: []string{"a", "b"}, RefColumns: []string{"x"},
	})

	ctrl := NewRelationshipPanelController(c, CoreDeps{}, ctx, tree, h, fwd, fkList(), nil)
	ctrl.SetPreviewResolver(func(context.Context, models.ForeignKey, []any) (any, error) {
		t.Fatal("preview resolver called for a mismatched composite FK")
		return nil, nil
	})

	if err := registerAndDispatch(t, ctrl, "relationship_panel.toggle"); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	body := ctrl.renderBodyFromState()
	if !strings.Contains(body, "-> parent (a=1, b=2)") {
		t.Fatalf("expected raw fallback for mismatched composite FK; got:\n%s", body)
	}
}

func TestRelationshipPanelPreviewCachedNoSecondQuery(t *testing.T) {
	c := testCommon(t)
	ctx, _ := newPanelContext(t)
	tree := &fakePanelTree{}
	h := newTabWithRow(t, "public.orders", []string{"id", "customer_id"}, []any{7, 42})
	fwd := fkList(models.ForeignKey{
		RefSchema: "public", RefTable: "customers",
		Columns: []string{"customer_id"}, RefColumns: []string{"id"},
	})

	rec := &recordingPreview{val: "Acme Corp"}
	ctrl := NewRelationshipPanelController(c, CoreDeps{}, ctx, tree, h, fwd, fkList(), nil)
	ctrl.SetPreviewResolver(rec.fn())

	if err := registerAndDispatch(t, ctrl, "relationship_panel.toggle"); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("first open issued %d preview queries, want 1", rec.count())
	}
	// Re-render the SAME row (rebuild + fill): the cache hit means no new query.
	ctrl.renderBody()
	ctrl.startPreviewFill(ctrl.currentEpoch())
	if rec.count() != 1 {
		t.Fatalf("revisited row issued %d preview queries, want 1 (cached)", rec.count())
	}
}

// TestRelationshipPanelEnterJumpsSelectedRelationship proves Enter on a
// focused non-null outbound relationship reaches FKForwardHelper.Jump,
// targeting the selected FK's column: the runner receives a parameterized
// SELECT against the parent table and the jump list gets an entry.
func TestRelationshipPanelEnterJumpsSelectedRelationship(t *testing.T) {
	c := testCommon(t)
	ctx, _ := newPanelContext(t)
	tree := &fakePanelTree{}
	h := newTabWithRow(t, "public.orders", []string{"id", "customer_id"}, []any{7, 42})
	fk := models.ForeignKey{
		Name: "orders_customer_fk", RefSchema: "public", RefTable: "customers",
		Columns: []string{"customer_id"}, RefColumns: []string{"id"},
	}
	fwd := fkList(fk)

	runner := &panelFakeRunner{}
	jumpList := &panelFakeJumpList{}
	fkHelper := helpers.NewFKForwardHelper(helpers.FKForwardDeps{
		Cache:    &panelFakeCache{fks: []models.ForeignKey{fk}},
		JumpList: jumpList,
		Runner:   runner,
		Tabs:     &panelFakeTabs{},
	})

	ctrl := NewRelationshipPanelController(c, CoreDeps{}, ctx, tree, h, fwd, fkList(), nil)
	ctrl.SetFKForward(fkHelper)

	if err := registerAndDispatch(t, ctrl, "relationship_panel.toggle"); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	// First Enter enters the panel (focus grab); second Enter jumps.
	if err := registerAndDispatch(t, ctrl, "relationship_panel.enter"); err != nil {
		t.Fatalf("enter (focus): %v", err)
	}
	if !ctrl.IsFocused() {
		t.Fatal("panel did not enter focus mode after first Enter")
	}
	if err := registerAndDispatch(t, ctrl, "relationship_panel.enter"); err != nil {
		t.Fatalf("enter (jump): %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("runner.calls = %d, want 1 (Jump ran the parent SELECT)", runner.calls)
	}
	if !strings.Contains(runner.gotQuery.SQL, `"public"."customers"`) {
		t.Fatalf("Jump query did not target the parent table; got SQL: %q", runner.gotQuery.SQL)
	}
	if len(runner.gotQuery.Args) != 1 || runner.gotQuery.Args[0] != 42 {
		t.Fatalf("Jump query args = %v, want [42] (the selected FK value)", runner.gotQuery.Args)
	}
	if len(jumpList.pushed) != 1 {
		t.Fatalf("jump list pushed %d entries, want 1", len(jumpList.pushed))
	}
}

// TestRelationshipPanelSupersedeOnRowChange proves a cursor move to a new row
// rebuilds the outbound state for the new FK value (the supersede point), so
// the next fill resolves the new row, not the stale one.
func TestRelationshipPanelSupersedeOnRowChange(t *testing.T) {
	c := testCommon(t)
	ctx, _ := newPanelContext(t)
	tree := &fakePanelTree{}
	h := ui.NewResultTabsHelper(ui.ResultTabsHelperDeps{})
	if err := h.OpenResultTab("t", nil); err != nil {
		t.Fatalf("OpenResultTab: %v", err)
	}
	tab := h.Active()
	g := tab.Grid()
	g.SetColumns([]models.ColumnMeta{{Name: "id"}, {Name: "customer_id"}})
	g.AppendRows([]models.Row{
		{Values: []any{1, 100}},
		{Values: []any{2, 200}},
	})
	tab.SetIdentity("conn1", query.ResultIdentity{BaseTable: "public.orders", HasRowIdentity: true})

	fwd := fkList(models.ForeignKey{
		RefSchema: "public", RefTable: "customers",
		Columns: []string{"customer_id"}, RefColumns: []string{"id"},
	})
	rec := &recordingPreview{val: "X"}
	ctrl := NewRelationshipPanelController(c, CoreDeps{}, ctx, tree, h, fwd, fkList(), nil)
	ctrl.SetPreviewResolver(rec.fn())

	if err := registerAndDispatch(t, ctrl, "relationship_panel.toggle"); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	// Opened on row 0 (customer_id=100).
	if calls := rec.snapshot(); len(calls) != 1 || calls[0][0] != 100 {
		t.Fatalf("first fill calls = %v, want one call with value 100", calls)
	}
	// Move to row 1 then settle: rebuild resolves customer_id=200.
	h.SetOnGridCursorChange(ctrl.NotifyCursorChange)
	g.MoveCursorDown()
	time.Sleep(relationshipPanelDebounce + 100*time.Millisecond)
	calls := rec.snapshot()
	if len(calls) < 2 {
		t.Fatalf("expected a second fill after row change; calls = %v", calls)
	}
	last := calls[len(calls)-1]
	if last[0] != 200 {
		t.Fatalf("post-move fill value = %v, want 200 (new row, not stale)", last[0])
	}
}

// --- local fakes (controllers package; the helpers_test fakes are in a
// different test package and not importable here) -------------------------

type panelFakeCache struct{ fks []models.ForeignKey }

func (f *panelFakeCache) Get(context.Context, string, string) ([]models.ForeignKey, error) {
	return f.fks, nil
}

type panelFakeRunner struct {
	gotQuery models.Query
	calls    int
}

func (f *panelFakeRunner) RunQuery(_ context.Context, q models.Query) (*session.RunHandle, error) {
	f.gotQuery = q
	f.calls++
	return nil, nil
}

type panelFakeTabs struct{}

func (panelFakeTabs) OpenResultTab(string, *session.RunHandle) error { return nil }

type panelFakeJumpList struct{ pushed []ui.JumpEntry }

func (f *panelFakeJumpList) Push(e ui.JumpEntry) { f.pushed = append(f.pushed, e) }
