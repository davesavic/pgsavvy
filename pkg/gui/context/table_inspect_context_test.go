package context

import (
	"strings"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// newInspectBase builds the BaseContext the container and leaves share. The
// ViewName is TableInspectViewName so the composed core's HandleRender (which
// reads its OWN GetViewName) and both leaves all target the same view — the
// invariant T2 must preserve (BLANK popup otherwise).
func newInspectBase() BaseContext {
	return NewBaseContext(BaseContextOpts{
		Key:      types.TABLE_INSPECT,
		ViewName: TableInspectViewName,
		Kind:     types.TEMPORARY_POPUP,
		Title:    "Table inspect",
	})
}

// newTestTableInspect builds a TableInspectContext with the supplied driver and
// wires real COLUMNS/INDEXES leaves, mirroring the setup.go assign closure
// (SetLeaves at construction). The leaves carry TableInspectViewName so their
// SetContent targets the shared view.
func newTestTableInspect(drv types.GuiDriver) *TableInspectContext {
	deps := Deps{GuiDriver: drv}
	c := NewTableInspectContext(newInspectBase(), deps)

	leafBase := func(key types.ContextKey, title string) BaseContext {
		return NewBaseContext(BaseContextOpts{
			Key:      key,
			ViewName: TableInspectViewName,
			Kind:     types.STUB,
			Title:    title,
		})
	}
	cols := NewColumnsContext(leafBase(types.COLUMNS, "Columns"), deps)
	idxs := NewIndexesContext(leafBase(types.INDEXES, "Indexes"), deps)
	c.SetLeaves(cols, idxs)
	return c
}

// newRecorderWithInspectView returns a recorder driver with a real gocui view
// installed for the inspect view, so HandleRender's SetContent and the
// layout-style origin clamp have a buffer/view to act on.
func newRecorderWithInspectView() *testfake.RecorderGuiDriver {
	drv := testfake.NewRecorderGuiDriver()
	drv.SetRealView(TableInspectViewName, gocui.NewView(TableInspectViewName, 0, 0, 40, 12, gocui.OutputNormal))
	return drv
}

// applyInspectScrollLike mirrors orchestrator.applyTableInspectScroll: after
// HandleRender it pins the inspect view's origin to the ACTIVE tab's scroll
// offsets (top-left clamp owned by the context, exercised here without the
// content-extent clamp the layout adds — the boundary test below proves the
// context's own clamp). The orchestrator owns the real clamp; this helper only
// proves the context's per-tab scroll feeds the view origin.
func applyInspectScrollLike(t *testing.T, drv *testfake.RecorderGuiDriver, c *TableInspectContext) {
	t.Helper()
	v := drv.RealView(TableInspectViewName)
	if v == nil {
		t.Fatalf("no real inspect view installed")
	}
	v.SetOrigin(c.ScrollX(), c.ScrollY())
}

func TestNewTableInspectContext_Kinds(t *testing.T) {
	c := newTestTableInspect(nil)
	if got := c.GetKey(); got != types.TABLE_INSPECT {
		t.Errorf("GetKey() = %q, want %q", got, types.TABLE_INSPECT)
	}
	if got := c.GetKind(); got != types.TEMPORARY_POPUP {
		t.Errorf("GetKind() = %d, want %d", got, types.TEMPORARY_POPUP)
	}
	if got := c.GetViewName(); got != TableInspectViewName {
		t.Errorf("GetViewName() = %q, want %q", got, TableInspectViewName)
	}
}

// TestTableInspectContext_Composition asserts the embed: the outer view name,
// the embedded core's view name, and BOTH leaves' view names all equal the
// constant (the BLANK-popup invariant). TabCount is promoted from the core.
func TestTableInspectContext_Composition(t *testing.T) {
	c := newTestTableInspect(nil)
	if c.TabbedRailContext == nil {
		t.Fatal("TableInspectContext does not embed *TabbedRailContext")
	}
	// The embedded core's HandleRender reads ITS OWN GetViewName, so the core
	// must carry the constant (BLANK-popup invariant). Bind the embedded pointer
	// to a typed local to assert against the core explicitly.
	core := c.TabbedRailContext
	if got := core.GetViewName(); got != TableInspectViewName {
		t.Errorf("core.GetViewName() = %q, want %q", got, TableInspectViewName)
	}
	if got := c.GetViewName(); got != TableInspectViewName {
		t.Errorf("outer.GetViewName() = %q, want %q", got, TableInspectViewName)
	}
	if got := c.TabCount(); got != 4 {
		t.Fatalf("TabCount() = %d, want 4 (Columns, Indexes, Foreign Keys, Constraints)", got)
	}
}

// TestTableInspectContext_LeavesShareViewAndRender proves both leaves declare a
// non-no-op HandleRender writing to the constant view (STUB is exempt from the
// wiring_invariant renderableKinds sweep, so this covers it directly).
func TestTableInspectContext_LeavesShareViewAndRender(t *testing.T) {
	drv := newRecorderWithInspectView()
	deps := Deps{GuiDriver: drv}

	cols := NewColumnsContext(NewBaseContext(BaseContextOpts{
		Key: types.COLUMNS, ViewName: TableInspectViewName, Kind: types.STUB,
	}), deps)
	idxs := NewIndexesContext(NewBaseContext(BaseContextOpts{
		Key: types.INDEXES, ViewName: TableInspectViewName, Kind: types.STUB,
	}), deps)

	if cols.GetViewName() != TableInspectViewName || idxs.GetViewName() != TableInspectViewName {
		t.Fatalf("leaf view names = (%q, %q), want both %q", cols.GetViewName(), idxs.GetViewName(), TableInspectViewName)
	}

	if err := cols.HandleRender(); err != nil {
		t.Fatalf("ColumnsContext.HandleRender: %v", err)
	}
	if got := drv.GetViewBuffer(TableInspectViewName); got != "(no columns)" {
		t.Errorf("empty columns body = %q, want %q", got, "(no columns)")
	}

	if err := idxs.HandleRender(); err != nil {
		t.Fatalf("IndexesContext.HandleRender: %v", err)
	}
	if got := drv.GetViewBuffer(TableInspectViewName); got != "(no indexes)" {
		t.Errorf("empty indexes body = %q, want %q", got, "(no indexes)")
	}
}

func TestTableInspectContext_HandleRender_Loading(t *testing.T) {
	drv := newRecorderWithInspectView()
	c := newTestTableInspect(drv)
	c.SetLoading(true)

	// Loading wins even when the active leaf has items.
	c.TabbedRailContext.tabs[0].leaf.(*ColumnsContext).SetItems([]any{
		&models.Column{Name: "id", DataType: "int"},
	})

	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if got := drv.GetViewBuffer(TableInspectViewName); !strings.HasPrefix(got, "Loading") {
		t.Errorf("loading body = %q; want prefix \"Loading\"", got)
	}
}

// TestTableInspectContext_HandleRender_DelegatesToActiveLeaf proves the loading
// gate, when off, delegates to the core which renders ONLY the active leaf.
func TestTableInspectContext_HandleRender_DelegatesToActiveLeaf(t *testing.T) {
	drv := newRecorderWithInspectView()
	c := newTestTableInspect(drv)
	c.SetLoading(false)

	cols := c.TabbedRailContext.tabs[0].leaf.(*ColumnsContext)
	idxs := c.TabbedRailContext.tabs[1].leaf.(*IndexesContext)
	cols.SetItems([]any{&models.Column{Name: "id", DataType: "int", Nullable: false}})
	idxs.SetItems([]any{&models.Index{Name: "pk_users"}})

	// Active tab 0 (Columns): body carries the column header + the column name.
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender (columns): %v", err)
	}
	body := drv.GetViewBuffer(TableInspectViewName)
	if !strings.Contains(body, "id") || !strings.Contains(body, "NAME") {
		t.Errorf("columns body = %q; want column header + name", body)
	}
	if strings.Contains(body, "pk_users") {
		t.Errorf("columns frame leaked the indexes leaf: %q", body)
	}

	// Switch to Indexes: body carries the index name.
	c.SetActiveTab(1)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender (indexes): %v", err)
	}
	body = drv.GetViewBuffer(TableInspectViewName)
	if !strings.Contains(body, "pk_users") {
		t.Errorf("indexes body = %q; want index name", body)
	}
}

// TestTableInspectContext_HandleRender_StatsHeader proves the stats (estimated
// rows/size) render as the FIRST body line above the active leaf's content once
// loading is off — the body-header topology that replaced the colliding
// top-border subtitle (the 4-tab strip left no room for it). The leaf content
// must still follow beneath the header.
func TestTableInspectContext_HandleRender_StatsHeader(t *testing.T) {
	drv := newRecorderWithInspectView()
	c := newTestTableInspect(drv)
	c.SetLoading(false)
	c.SetTarget("public", "users")

	tbl := &models.Table{Schema: "public", Name: "users"}
	tbl.EstimatedRows.Store(12000)
	tbl.SizeBytes.Store(4194304)
	c.SetStats(tbl)

	cols := c.TabbedRailContext.tabs[0].leaf.(*ColumnsContext)
	cols.SetItems([]any{&models.Column{Name: "id", DataType: "int"}})

	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.GetViewBuffer(TableInspectViewName)
	lines := strings.Split(body, "\n")
	if len(lines) < 3 {
		t.Fatalf("body has %d lines, want >= 3 (header, blank, leaf): %q", len(lines), body)
	}
	if !strings.Contains(lines[0], "public.users") || !strings.Contains(lines[0], "~12k rows") {
		t.Errorf("first body line = %q; want stats header with table + ~12k rows", lines[0])
	}
	if lines[1] != "" {
		t.Errorf("second body line = %q; want blank line separating header from table", lines[1])
	}
	if !strings.Contains(body, "NAME") || !strings.Contains(body, "id") {
		t.Errorf("body missing leaf content below header: %q", body)
	}
}

// TestTableInspectContext_HandleRender_NoTargetNoHeader proves the body-header
// hook stays inert until a target is set: with no schema/table, HandleRender
// renders ONLY the leaf body (no leading "." header line).
func TestTableInspectContext_HandleRender_NoTargetNoHeader(t *testing.T) {
	drv := newRecorderWithInspectView()
	c := newTestTableInspect(drv)
	c.SetLoading(false)

	cols := c.TabbedRailContext.tabs[0].leaf.(*ColumnsContext)
	cols.SetItems([]any{&models.Column{Name: "id", DataType: "int"}})

	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.GetViewBuffer(TableInspectViewName)
	if !strings.HasPrefix(body, "NAME") {
		t.Errorf("body = %q; want leaf content with no stats header (prefix NAME)", body)
	}
}

// TestTableInspectContext_PerTabScroll_IndependentAcrossTabs proves each tab's
// scroll is stored independently and feeds the (layout-style) view origin, and
// that switching back restores tab0's offset.
func TestTableInspectContext_PerTabScroll_IndependentAcrossTabs(t *testing.T) {
	drv := newRecorderWithInspectView()
	c := newTestTableInspect(drv)
	v := drv.RealView(TableInspectViewName)

	// Tab 0: scroll to (3, 2), render, clamp into the view origin.
	c.SetScrollX(3)
	c.SetScrollY(2)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender tab0: %v", err)
	}
	applyInspectScrollLike(t, drv, c)
	if ox, oy := v.Origin(); ox != 3 || oy != 2 {
		t.Fatalf("tab0 origin = (%d,%d), want (3,2)", ox, oy)
	}

	// Switch to tab 1: its scroll is independent and starts at (0,0).
	c.SetActiveTab(1)
	if c.ScrollX() != 0 || c.ScrollY() != 0 {
		t.Fatalf("tab1 scroll = (%d,%d), want (0,0) (independent)", c.ScrollX(), c.ScrollY())
	}
	c.SetScrollX(5)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender tab1: %v", err)
	}
	applyInspectScrollLike(t, drv, c)
	if ox, _ := v.Origin(); ox != 5 {
		t.Fatalf("tab1 origin x = %d, want 5", ox)
	}

	// Switch back to tab 0: its (3,2) offset is preserved.
	c.SetActiveTab(0)
	if c.ScrollX() != 3 || c.ScrollY() != 2 {
		t.Fatalf("tab0 scroll after switch-back = (%d,%d), want (3,2)", c.ScrollX(), c.ScrollY())
	}
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender tab0 (return): %v", err)
	}
	applyInspectScrollLike(t, drv, c)
	if ox, oy := v.Origin(); ox != 3 || oy != 2 {
		t.Errorf("tab0 origin after switch-back = (%d,%d), want (3,2)", ox, oy)
	}
}

// TestTableInspectContext_SetTarget_ZeroesAllTabs proves SetTarget resets every
// tab's stored scroll so a re-open on a new table lands top-left on BOTH tabs.
func TestTableInspectContext_SetTarget_ZeroesAllTabs(t *testing.T) {
	c := newTestTableInspect(nil)

	// Dirty both tabs' scroll.
	c.SetActiveTab(0)
	c.SetScrollX(4)
	c.SetScrollY(1)
	c.SetActiveTab(1)
	c.SetScrollX(7)
	c.SetScrollY(9)

	c.SetTarget("public", "users")

	c.SetActiveTab(0)
	if c.ScrollX() != 0 || c.ScrollY() != 0 {
		t.Errorf("tab0 scroll after SetTarget = (%d,%d), want (0,0)", c.ScrollX(), c.ScrollY())
	}
	c.SetActiveTab(1)
	if c.ScrollX() != 0 || c.ScrollY() != 0 {
		t.Errorf("tab1 scroll after SetTarget = (%d,%d), want (0,0)", c.ScrollX(), c.ScrollY())
	}
}

func TestTableInspectContext_Target_RoundTrip(t *testing.T) {
	c := newTestTableInspect(nil)
	if s, tb := c.Target(); s != "" || tb != "" {
		t.Errorf("zero Target() = (%q, %q); want (\"\", \"\")", s, tb)
	}
	c.SetTarget("public", "users")
	if s, tb := c.Target(); s != "public" || tb != "users" {
		t.Errorf("Target() = (%q, %q); want (\"public\", \"users\")", s, tb)
	}
}

func TestTableInspectContext_LoadingAccessors(t *testing.T) {
	c := newTestTableInspect(nil)
	if c.IsLoading() {
		t.Error("IsLoading() = true at construction; want false")
	}
	c.SetLoading(true)
	if !c.IsLoading() {
		t.Error("IsLoading() = false after SetLoading(true)")
	}
}

// TestTableInspectContext_ScrollClampAtZero proves the context's top-left clamp:
// negative absolute scroll settles at 0.
func TestTableInspectContext_ScrollClampAtZero(t *testing.T) {
	c := newTestTableInspect(nil)
	c.SetScrollX(-5)
	c.SetScrollY(-3)
	if c.ScrollX() != 0 || c.ScrollY() != 0 {
		t.Errorf("scroll after negative set = (%d,%d), want (0,0)", c.ScrollX(), c.ScrollY())
	}
	c.SetScrollX(4)
	c.Scroll(-10, -10)
	if c.ScrollX() != 0 || c.ScrollY() != 0 {
		t.Errorf("scroll after relative under-scroll = (%d,%d), want (0,0)", c.ScrollX(), c.ScrollY())
	}
}

// TestTableInspectContext_NoPanic exercises the nil-driver and unwired-leaf
// boundaries: HandleRender (loading + delegating) and the scroll accessors must
// not panic.
func TestTableInspectContext_NoPanic(t *testing.T) {
	// nil driver, loading path.
	c := newTestTableInspect(nil)
	c.SetLoading(true)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender nil driver (loading): %v", err)
	}

	// nil driver, delegate path.
	c.SetLoading(false)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender nil driver (delegate): %v", err)
	}

	// Container with no leaves wired: scroll accessors and HandleRender are safe.
	bare := NewTableInspectContext(newInspectBase(), Deps{GuiDriver: nil})
	bare.SetScrollX(2)
	bare.SetScrollY(2)
	if bare.ScrollX() != 2 || bare.ScrollY() != 2 {
		t.Errorf("bare scroll = (%d,%d), want (2,2)", bare.ScrollX(), bare.ScrollY())
	}
	if err := bare.HandleRender(); err != nil {
		t.Fatalf("bare HandleRender: %v", err)
	}
}

// TestTableInspectContext_SatisfiesScroller proves the context still satisfies
// the layout's tableInspectScroller seam (ScrollX/ScrollY/SetScrollX/SetScrollY)
// so applyTableInspectScroll keeps type-asserting onto it.
func TestTableInspectContext_SatisfiesScroller(t *testing.T) {
	type tableInspectScroller interface {
		ScrollX() int
		ScrollY() int
		SetScrollX(int)
		SetScrollY(int)
	}
	var _ tableInspectScroller = newTestTableInspect(nil)
}

// TestBytesHuman asserts the base-1024 humanizer: bytes < 1024 render exact
// with no decimal; KB and up render with exactly one decimal place.
func TestBytesHuman(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{512, "512 B"},
		{1536, "1.5 KB"},
		{4194304, "4.0 MB"},
		{2147483648, "2.0 GB"},
		{0, "0 B"},
	}
	for _, tc := range cases {
		if got := bytesHuman(tc.in); got != tc.want {
			t.Errorf("bytesHuman(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestHumanizeRows asserts the compact row-estimate formatter, mirroring
// controllers.humanizeEstimate's style (12000 -> "12k").
func TestHumanizeRows(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1200, "1.2k"},
		{12000, "12k"},
		{1_500_000, "1.5M"},
		{-1, "0"},
	}
	for _, tc := range cases {
		if got := humanizeRows(tc.in); got != tc.want {
			t.Errorf("humanizeRows(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestStatsLine_Suppression asserts the stats segment is omitted (schema.table
// only) for never-analyzed (rows = -1) and not-yet-loaded (rows = 0 && bytes =
// 0) tables, so the popup never renders the "~0 rows · 0 B" lie.
func TestStatsLine_Suppression(t *testing.T) {
	c := newTestTableInspect(nil)
	c.SetTarget("public", "users")

	// No table set yet: schema.table only.
	if got := c.StatsLine(); got != "public.users" {
		t.Errorf("nil tbl: StatsLine() = %q, want %q", got, "public.users")
	}

	neverAnalyzed := &models.Table{Schema: "public", Name: "users"}
	neverAnalyzed.EstimatedRows.Store(-1)
	c.SetStats(neverAnalyzed)
	if got := c.StatsLine(); got != "public.users" {
		t.Errorf("rows=-1: StatsLine() = %q, want %q", got, "public.users")
	}

	notLoaded := &models.Table{Schema: "public", Name: "users"}
	notLoaded.EstimatedRows.Store(0)
	notLoaded.SizeBytes.Store(0)
	c.SetStats(notLoaded)
	if got := c.StatsLine(); got != "public.users" {
		t.Errorf("rows=0&&bytes=0: StatsLine() = %q, want %q", got, "public.users")
	}
}

// TestStatsLine_Composed asserts a loaded table renders the schema.table plus a
// ~N-rows form and a humanized size, read LIVE from the atomic counters.
func TestStatsLine_Composed(t *testing.T) {
	c := newTestTableInspect(nil)
	c.SetTarget("public", "users")

	tbl := &models.Table{Schema: "public", Name: "users"}
	tbl.EstimatedRows.Store(12000)
	tbl.SizeBytes.Store(4194304)
	c.SetStats(tbl)

	got := c.StatsLine()
	if !strings.HasPrefix(got, "public.users") {
		t.Errorf("StatsLine() = %q, want prefix %q", got, "public.users")
	}
	if !strings.Contains(got, "~12k rows") {
		t.Errorf("StatsLine() = %q, want it to contain %q", got, "~12k rows")
	}
	if !strings.Contains(got, "4.0 MB") {
		t.Errorf("StatsLine() = %q, want it to contain %q", got, "4.0 MB")
	}

	// LIVE read: a later async enrichment must reflect without re-calling
	// SetStats (proves a reference, not a captured snapshot, is stored).
	tbl.EstimatedRows.Store(2_000_000)
	if got := c.StatsLine(); !strings.Contains(got, "~2M rows") {
		t.Errorf("live re-read: StatsLine() = %q, want it to contain %q", got, "~2M rows")
	}
}
