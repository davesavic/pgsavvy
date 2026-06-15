package controllers

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/query"
)

// fakePanelTree records Push/PopIfTop and reports membership for Stack().
type fakePanelTree struct {
	mu     sync.Mutex
	stack  []types.IBaseContext
	pushes int
	pops   int
}

func (f *fakePanelTree) Push(c types.IBaseContext) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stack = append(f.stack, c)
	f.pushes++
	return nil
}

func (f *fakePanelTree) PopIfTop(key types.ContextKey) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n := len(f.stack); n > 0 && f.stack[n-1].GetKey() == key {
		f.stack = f.stack[:n-1]
		f.pops++
	}
	return nil
}

func (f *fakePanelTree) Stack() []types.IBaseContext {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]types.IBaseContext, len(f.stack))
	copy(out, f.stack)
	return out
}

func testCommon(t *testing.T) *common.Common {
	t.Helper()
	fs := afero.NewMemMapFs()
	cfg := config.GetDefaultConfig()
	return common.NewCommon(slog.New(slog.DiscardHandler), i18n.EnglishTranslationSet(), cfg, &common.AppState{}, fs)
}

// newPanelContext builds a RELATIONSHIP_PANEL context with its view
// registered on a recorder driver so HandleRender records.
func newPanelContext(t *testing.T) (*guicontext.RelationshipPanelContext, *testfake.RecorderGuiDriver) {
	t.Helper()
	rec := testfake.NewRecorderGuiDriver()
	_, _ = rec.SetView(string(types.RELATIONSHIP_PANEL), 0, 0, 10, 10, 0)
	base := guicontext.NewBaseContext(guicontext.BaseContextOpts{
		Key:      types.RELATIONSHIP_PANEL,
		ViewName: string(types.RELATIONSHIP_PANEL),
		Kind:     types.DISPLAY_CONTEXT,
	})
	ctx := guicontext.NewRelationshipPanelContext(base, types.ContextTreeDeps{GuiDriver: rec})
	return ctx, rec
}

// newTabWithRow opens a result tab on the helper, populates its grid with
// the given columns + a single row, and stamps the identity. Returns the
// helper (which is the ResultTabsManager).
func newTabWithRow(t *testing.T, baseTable string, cols []string, row []any) *ui.ResultTabsHelper {
	t.Helper()
	h := ui.NewResultTabsHelper(ui.ResultTabsHelperDeps{})
	if err := h.OpenResultTab("t", nil); err != nil {
		t.Fatalf("OpenResultTab: %v", err)
	}
	tab := h.Active()
	if tab == nil {
		t.Fatal("Active() = nil after OpenResultTab")
	}
	g := tab.Grid()
	if g == nil {
		t.Fatal("tab has no grid")
	}
	meta := make([]models.ColumnMeta, len(cols))
	for i, c := range cols {
		meta[i] = models.ColumnMeta{Name: c}
	}
	g.SetColumns(meta)
	g.AppendRows([]models.Row{{Values: row}})
	tab.SetIdentity("conn1", query.ResultIdentity{BaseTable: baseTable, HasRowIdentity: true})
	return h
}

// fwdFK / revFK build static FK-lookup closures.
func fkList(fks ...models.ForeignKey) relationshipFKLookup {
	return func(_ context.Context, _, _ string) ([]models.ForeignKey, error) {
		return fks, nil
	}
}

func TestRelationshipPanelToggleOpensAndCloses(t *testing.T) {
	c := testCommon(t)
	ctx, _ := newPanelContext(t)
	tree := &fakePanelTree{}
	h := newTabWithRow(t, "public.orders", []string{"id", "customer_id"}, []any{7, 42})
	fwd := fkList(models.ForeignKey{RefTable: "customers", Columns: []string{"customer_id"}})

	ctrl := NewRelationshipPanelController(c, CoreDeps{}, ctx, tree, h, fwd, fkList(), nil)

	if ctrl.IsOpen() {
		t.Fatal("panel reports open before any toggle")
	}
	// First toggle opens (push + seed body).
	if err := registerAndDispatch(t, ctrl, "relationship_panel.toggle"); err != nil {
		t.Fatalf("toggle open: %v", err)
	}
	if tree.pushes != 1 || !ctrl.IsOpen() {
		t.Fatalf("after open: pushes=%d isOpen=%v, want 1/true", tree.pushes, ctrl.IsOpen())
	}
	if body := ctx.Body(); !strings.Contains(body, "-> customers (customer_id=42)") {
		t.Fatalf("seeded body missing outbound FK line; got:\n%s", body)
	}
	// Second toggle closes (pop).
	if err := registerAndDispatch(t, ctrl, "relationship_panel.toggle"); err != nil {
		t.Fatalf("toggle close: %v", err)
	}
	if tree.pops != 1 || ctrl.IsOpen() {
		t.Fatalf("after close: pops=%d isOpen=%v, want 1/false", tree.pops, ctrl.IsOpen())
	}
}

func TestRelationshipPanelToggleNoActiveTabIsNoOp(t *testing.T) {
	c := testCommon(t)
	ctx, _ := newPanelContext(t)
	tree := &fakePanelTree{}
	// Empty helper: no tabs.
	h := ui.NewResultTabsHelper(ui.ResultTabsHelperDeps{})

	ctrl := NewRelationshipPanelController(c, CoreDeps{}, ctx, tree, h, fkList(), fkList(), nil)
	if err := registerAndDispatch(t, ctrl, "relationship_panel.toggle"); err != nil {
		t.Fatalf("toggle with no tab: %v", err)
	}
	if tree.pushes != 0 || ctrl.IsOpen() {
		t.Fatalf("toggle with no active tab pushed the panel (pushes=%d)", tree.pushes)
	}
}

func TestRelationshipPanelEnterExitAreStubsNoPanic(t *testing.T) {
	c := testCommon(t)
	ctx, _ := newPanelContext(t)
	tree := &fakePanelTree{}
	h := newTabWithRow(t, "public.orders", []string{"id"}, []any{1})

	ctrl := NewRelationshipPanelController(c, CoreDeps{}, ctx, tree, h, fkList(), fkList(), nil)
	if err := registerAndDispatch(t, ctrl, "relationship_panel.enter"); err != nil {
		t.Fatalf("enter: %v", err)
	}
	if err := registerAndDispatch(t, ctrl, "relationship_panel.exit"); err != nil {
		t.Fatalf("exit: %v", err)
	}
}

func TestRelationshipPanelInboundOutboundBody(t *testing.T) {
	c := testCommon(t)
	ctx, _ := newPanelContext(t)
	tree := &fakePanelTree{}
	h := newTabWithRow(t, "public.customers", []string{"id"}, []any{42})
	fwd := fkList() // no outbound
	rev := fkList(models.ForeignKey{Table: "orders"}, models.ForeignKey{Table: "invoices"})

	ctrl := NewRelationshipPanelController(c, CoreDeps{}, ctx, tree, h, fwd, rev, nil)
	body := ctrl.renderBody()
	if !strings.Contains(body, "<- orders") || !strings.Contains(body, "<- invoices") {
		t.Fatalf("inbound lines missing; got:\n%s", body)
	}
}

func TestRelationshipPanelCapsAtTwelve(t *testing.T) {
	c := testCommon(t)
	ctx, _ := newPanelContext(t)
	tree := &fakePanelTree{}
	h := newTabWithRow(t, "public.t", []string{"id"}, []any{1})
	// 15 outbound FKs -> first 12 + "(+3 more)".
	fks := make([]models.ForeignKey, 15)
	for i := range fks {
		fks[i] = models.ForeignKey{RefTable: "ref", Columns: []string{"id"}}
	}
	ctrl := NewRelationshipPanelController(c, CoreDeps{}, ctx, tree, h, fkList(fks...), fkList(), nil)

	body := ctrl.renderBody()
	if n := strings.Count(body, "-> ref"); n != 12 {
		t.Fatalf("outbound lines = %d, want 12 (cap)", n)
	}
	if !strings.Contains(body, "(+3 more)") {
		t.Fatalf("overflow line missing; got:\n%s", body)
	}
}

func TestRelationshipPanelAllMotionsNotifyWithEpochDebounce(t *testing.T) {
	c := testCommon(t)
	ctx, _ := newPanelContext(t)
	tree := &fakePanelTree{}
	// Multi-row grid so motions actually move the cursor.
	h := ui.NewResultTabsHelper(ui.ResultTabsHelperDeps{})
	if err := h.OpenResultTab("t", nil); err != nil {
		t.Fatalf("OpenResultTab: %v", err)
	}
	tab := h.Active()
	g := tab.Grid()
	g.SetColumns([]models.ColumnMeta{{Name: "id"}})
	rows := make([]models.Row, 50)
	for i := range rows {
		rows[i] = models.Row{Values: []any{i}}
	}
	g.AppendRows(rows)
	tab.SetIdentity("conn1", query.ResultIdentity{BaseTable: "t", HasRowIdentity: true})

	ctrl := NewRelationshipPanelController(c, CoreDeps{}, ctx, tree, h, fkList(), fkList(), nil)
	// Open the panel so NotifyCursorChange is active.
	if err := registerAndDispatch(t, ctrl, "relationship_panel.toggle"); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	// Wire the live-follow hook onto the grid (mirrors the orchestrator).
	h.SetOnGridCursorChange(ctrl.NotifyCursorChange)

	base := ctrl.currentEpoch()
	// Drive a burst of DIFFERENT row-change motions within the debounce
	// window: each must bump the epoch (every motion notifies, not just
	// j/k).
	g.MoveCursorDown() // j
	g.MoveCursorDown() // j
	g.JumpLast()       // G
	g.HalfPageUp()     // <c-u>
	g.JumpFirst()      // gg
	g.SetCursor(10, 0) // jump-list nav
	if got := ctrl.currentEpoch() - base; got != 6 {
		t.Fatalf("epoch advanced by %d after 6 motions, want 6 (every motion notifies)", got)
	}

	// After the debounce settles, exactly ONE repaint runs (the latest
	// epoch), and the body reflects the final row.
	time.Sleep(relationshipPanelDebounce + 100*time.Millisecond)
	if ctx.Body() == "" {
		t.Fatal("panel body empty after debounce settle; expected a repaint")
	}
}

func TestRelationshipPanelTabCloseDropsRepaint(t *testing.T) {
	c := testCommon(t)
	ctx, _ := newPanelContext(t)
	tree := &fakePanelTree{}
	h := newTabWithRow(t, "public.t", []string{"id"}, []any{1})

	ctrl := NewRelationshipPanelController(c, CoreDeps{}, ctx, tree, h, fkList(), fkList(), nil)
	if err := registerAndDispatch(t, ctrl, "relationship_panel.toggle"); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	// Close the panel (pop) — a repaint armed before close must not render.
	if err := registerAndDispatch(t, ctrl, "relationship_panel.toggle"); err != nil {
		t.Fatalf("toggle close: %v", err)
	}
	// Arm a stale repaint at the current epoch, then confirm IsOpen()==false
	// keeps it from re-rendering (no panic, graceful drop).
	ctrl.NotifyCursorChange(0, 0) // no-op: panel closed, IsOpen()==false
	time.Sleep(relationshipPanelDebounce + 50*time.Millisecond)
	if ctrl.IsOpen() {
		t.Fatal("panel reports open after close")
	}
}

// registerAndDispatch registers the controller's actions into a fresh
// registry and invokes the handler registered for id.
func registerAndDispatch(t *testing.T, ctrl *RelationshipPanelController, id string) error {
	t.Helper()
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	cmd, ok := reg.Get(id)
	if !ok || cmd == nil || cmd.Handler == nil {
		t.Fatalf("no handler registered for %q", id)
	}
	return cmd.Handler(commands.ExecCtx{})
}
