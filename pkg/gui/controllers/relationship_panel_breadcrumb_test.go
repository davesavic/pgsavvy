package controllers

import (
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/query"
)

// fakeBreadcrumbJumps is a read-only jump-list snapshot source: it returns the
// configured entries + cursor verbatim, mirroring *ui.ResultJumpList.Snapshot.
type fakeBreadcrumbJumps struct {
	entries []ui.JumpEntry
	cursor  int
}

func (f fakeBreadcrumbJumps) Snapshot() ([]ui.JumpEntry, int) {
	out := make([]ui.JumpEntry, len(f.entries))
	copy(out, f.entries)
	return out, f.cursor
}

// fakeBreadcrumbLabels maps an open TabID to its label; a missing id reports
// the tab as closed (open=false), exercising the prune/dangling path.
type fakeBreadcrumbLabels map[string]string

func (f fakeBreadcrumbLabels) TabLabelByID(tabID string) (string, bool) {
	label, ok := f[tabID]
	return label, ok
}

// newBreadcrumbCtrl builds a panel controller over a single active tab labeled
// activeLabel, wired with the given breadcrumb sources. The active tab carries
// no FKs (the breadcrumb is the line under test).
func newBreadcrumbCtrl(t *testing.T, activeLabel string, jumps breadcrumbJumpList, labels breadcrumbTabLabels) *RelationshipPanelController {
	t.Helper()
	c := testCommon(t)
	ctx, _ := newPanelContext(t)
	tree := &fakePanelTree{}
	h := ui.NewResultTabsHelper(ui.ResultTabsHelperDeps{})
	if err := h.OpenResultTab(activeLabel, nil); err != nil {
		t.Fatalf("OpenResultTab: %v", err)
	}
	// Stamp a base-table identity so renderBodyFromState reaches the body (and
	// the breadcrumb header) rather than the "(no relationships)" early return.
	h.Active().SetIdentity("conn1", query.ResultIdentity{BaseTable: "public.t", HasRowIdentity: true})
	ctrl := NewRelationshipPanelController(c, CoreDeps{}, ctx, tree, h, fkList(), fkList(), nil)
	ctrl.SetBreadcrumb(jumps, labels)
	return ctrl
}

// TestRelationshipPanelBreadcrumbReflectsPath: after jumping FROM customers INTO
// the active tab (orders), the breadcrumb shows "customers -> [orders]" with the
// active tab marked (cursor == -1, at most-recent).
func TestRelationshipPanelBreadcrumbReflectsPath(t *testing.T) {
	labels := fakeBreadcrumbLabels{"7": "customers"}
	jumps := fakeBreadcrumbJumps{
		entries: []ui.JumpEntry{{TabID: "7"}},
		cursor:  -1,
	}
	ctrl := newBreadcrumbCtrl(t, "orders", jumps, labels)

	body := ctrl.renderBody()
	first := firstLine(body)
	if first != "customers -> [orders]" {
		t.Fatalf("breadcrumb = %q, want %q\nbody:\n%s", first, "customers -> [orders]", body)
	}
}

// TestRelationshipPanelBreadcrumbBackMarkerMoves: with the same two-step path,
// a jump-list cursor of 0 (after <c-o>) marks the customers segment instead of
// the active orders segment — consistent with jump-list Back.
func TestRelationshipPanelBreadcrumbBackMarkerMoves(t *testing.T) {
	labels := fakeBreadcrumbLabels{"7": "customers"}
	jumps := fakeBreadcrumbJumps{
		entries: []ui.JumpEntry{{TabID: "7"}},
		cursor:  0, // <c-o> moved the cursor onto the customers entry
	}
	ctrl := newBreadcrumbCtrl(t, "orders", jumps, labels)

	first := firstLine(ctrl.renderBody())
	if first != "[customers] -> orders" {
		t.Fatalf("breadcrumb after back = %q, want %q", first, "[customers] -> orders")
	}
}

// TestRelationshipPanelBreadcrumbEmpty: no jumps -> muted empty state, no crash.
func TestRelationshipPanelBreadcrumbEmpty(t *testing.T) {
	ctrl := newBreadcrumbCtrl(t, "orders", fakeBreadcrumbJumps{cursor: -1}, fakeBreadcrumbLabels{})

	first := firstLine(ctrl.renderBody())
	if first != "(no path)" {
		t.Fatalf("empty breadcrumb = %q, want %q", first, "(no path)")
	}
}

// TestRelationshipPanelBreadcrumbPruneOnClose: an entry whose tab has closed is
// skipped (reported closed by TabLabelByID) — no dangling segment, no panic.
func TestRelationshipPanelBreadcrumbPruneOnClose(t *testing.T) {
	// Two entries: tab "7" (customers) still open, tab "8" (invoices) closed.
	labels := fakeBreadcrumbLabels{"7": "customers"}
	jumps := fakeBreadcrumbJumps{
		entries: []ui.JumpEntry{{TabID: "7"}, {TabID: "8"}},
		cursor:  -1,
	}
	ctrl := newBreadcrumbCtrl(t, "orders", jumps, labels)

	first := firstLine(ctrl.renderBody())
	if first != "customers -> [orders]" {
		t.Fatalf("breadcrumb with closed middle tab = %q, want %q (no dangling)", first, "customers -> [orders]")
	}
	if strings.Contains(first, "invoices") || strings.Contains(first, "8") {
		t.Fatalf("closed tab leaked into breadcrumb: %q", first)
	}
}

// TestRelationshipPanelBreadcrumbSelfRef: a self-referential revisit
// (employees -> employees, e.g. manager_id -> id) renders both segments using
// their tab labels, distinguished by the label content, without error.
func TestRelationshipPanelBreadcrumbSelfRef(t *testing.T) {
	// Two prior employees tabs (different labels encode the relationship),
	// active tab is a third employees view.
	labels := fakeBreadcrumbLabels{
		"1": "employees",
		"2": "-> employees(manager_id)",
	}
	jumps := fakeBreadcrumbJumps{
		entries: []ui.JumpEntry{{TabID: "1"}, {TabID: "2"}},
		cursor:  -1,
	}
	ctrl := newBreadcrumbCtrl(t, "-> employees(manager_id)", jumps, labels)

	first := firstLine(ctrl.renderBody())
	want := "employees -> -> employees(manager_id) -> [-> employees(manager_id)]"
	if first != want {
		t.Fatalf("self-ref breadcrumb = %q, want %q", first, want)
	}
}

// TestRelationshipPanelBreadcrumbAbsentWhenUnwired: with no breadcrumb sources
// wired (T1-T4 default), no breadcrumb line is rendered — the body still opens
// with the outbound header.
func TestRelationshipPanelBreadcrumbAbsentWhenUnwired(t *testing.T) {
	ctrl := newBreadcrumbCtrl(t, "orders", nil, nil)
	first := firstLine(ctrl.renderBody())
	if strings.Contains(first, "->") && !strings.Contains(first, "Outbound") {
		t.Fatalf("unexpected breadcrumb line when unwired: %q", first)
	}
	if !strings.HasPrefix(firstLine(ctrl.renderBody()), "Outbound") {
		t.Fatalf("first line = %q, want outbound header (no breadcrumb)", first)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
