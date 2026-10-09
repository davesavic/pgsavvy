package controllers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/query"
	"github.com/stretchr/testify/require"
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

type reviewCountUI struct {
	mu   sync.Mutex
	jobs []func() error
}

func (p *reviewCountUI) post(fn func() error) {
	p.mu.Lock()
	p.jobs = append(p.jobs, fn)
	p.mu.Unlock()
}

func (p *reviewCountUI) drain(t *testing.T) {
	t.Helper()
	for {
		p.mu.Lock()
		jobs := p.jobs
		p.jobs = nil
		p.mu.Unlock()
		if len(jobs) == 0 {
			return
		}
		for _, fn := range jobs {
			require.NoError(t, fn())
		}
	}
}

func TestReview_relationship_count_completion_order(t *testing.T) {
	for _, order := range []string{"estimate_first", "exact_first"} {
		for _, estimateFails := range []bool{false, true} {
			for _, exactStatus := range []string{"success", "error", "timeout"} {
				t.Run(fmt.Sprintf("%s/estimate_error=%t/exact_%s", order, estimateFails, exactStatus), func(t *testing.T) {
					ctrl, _ := inboundOnlyTabAndRev(t, 42)
					pump := &reviewCountUI{}
					ctrl.onUIContentOnly = pump.post
					var jobs []func()
					ctrl.SetOnWorker(func(fn func()) { jobs = append(jobs, fn) })
					var estimateErr, exactErr error
					if estimateFails {
						estimateErr = errors.New("synthetic estimate error")
					}
					switch exactStatus {
					case "error":
						exactErr = errors.New("synthetic exact error")
					case "timeout":
						exactErr = context.DeadlineExceeded
					}
					ctrl.SetEstimateResolver(estimateFn(1200, estimateErr))
					calls := 0
					ctrl.SetExactResolver(func(context.Context, models.ForeignKey, []any) (int64, error) { calls++; return 1187, exactErr })
					focusInbound(t, ctrl)
					require.Len(t, jobs, 2, "both requests must be admitted before either completes")
					if order == "estimate_first" {
						jobs[0]()
						jobs[1]()
					} else {
						jobs[1]()
						jobs[0]()
					}
					pump.drain(t)
					key := ctrl.inbound[0].estimateKey
					want := estimateResult{rows: 1200}
					if estimateFails {
						want = estimateResult{degraded: true}
					}
					switch exactStatus {
					case "success":
						want.exact, want.hasExact = 1187, true
					case "error":
						want.exactErr = true
					}
					require.Equal(t, want, ctrl.estimates[key])
					if exactStatus == "success" {
						require.Contains(t, ctrl.ctx.Body(), "1187")
						require.NotContains(t, ctrl.ctx.Body(), "~1.2k")
					}
					if exactStatus == "error" {
						require.Contains(t, ctrl.ctx.Body(), ctrl.tr().RelationshipPanelExactError)
					}
					if exactStatus == "timeout" {
						if estimateFails {
							require.Contains(t, ctrl.ctx.Body(), ctrl.tr().RelationshipPanelEstimateError)
						} else {
							require.Contains(t, ctrl.ctx.Body(), "~1.2k")
						}
						require.NotContains(t, ctrl.ctx.Body(), "1187")
						require.NotContains(t, ctrl.ctx.Body(), ctrl.tr().RelationshipPanelExactError)
					}
					jobs = nil
					require.NoError(t, registerAndDispatch(t, ctrl, commands.RelationshipPanelExit))
					require.NoError(t, registerAndDispatch(t, ctrl, commands.RelationshipPanelEnter))
					if exactStatus == "timeout" {
						require.Len(t, jobs, 1)
						jobs[0]()
						require.Equal(t, 2, calls)
					} else {
						require.Empty(t, jobs)
						require.Equal(t, 1, calls)
					}
					pump.drain(t)
				})
			}
		}
	}
}

func TestReview_relationship_count_stale_completion(t *testing.T) {
	for _, source := range []string{"estimate", "exact"} {
		for _, invalidate := range []string{"row", "tab", "clear", "table_eviction", "unrelated_eviction", "all_eviction", "close", "close_reopen", "result_close", "result_close_all"} {
			t.Run(source+"/"+invalidate, func(t *testing.T) {
				ctrl, _ := inboundOnlyTabAndRev(t, 42)
				pump := &reviewCountUI{}
				ctrl.onUIContentOnly = pump.post
				var jobs []func()
				ctrl.SetOnWorker(func(fn func()) { jobs = append(jobs, fn) })
				arrived, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				lookup := func(context.Context, models.ForeignKey, []any) (int64, error) {
					close(arrived)
					<-release
					return 1187, nil
				}
				if source == "estimate" {
					ctrl.SetEstimateResolver(lookup)
				} else {
					ctrl.SetExactResolver(lookup)
				}
				focusInbound(t, ctrl)
				require.Len(t, jobs, 1)
				key := ctrl.inbound[0].estimateKey
				ctrl.estimates["unrelated_completed"] = estimateResult{exact: 99, hasExact: true}
				go func() { defer close(finished); jobs[0]() }()
				t.Cleanup(func() {
					unblock()
					<-finished
					ctrl.mu.Lock()
					if ctrl.timer != nil {
						ctrl.timer.Stop()
					}
					ctrl.mu.Unlock()
				})
				select {
				case <-arrived:
				case <-time.After(time.Second):
					t.Fatal("count lookup did not start")
				}
				switch invalidate {
				case "row":
					grid := ctrl.mgr.Active().Grid()
					grid.SetColumns(grid.Columns())
					grid.AppendRows([]models.Row{{Values: []any{int64(43)}}})
					ctrl.NotifyCursorChange(1, 0)
				case "tab":
					_, identity := ctrl.mgr.Active().Identity()
					ctrl.mgr.Active().SetIdentity("new_connection", identity)
					ctrl.NotifyActiveTabChanged()
				case "clear":
					ctrl.ClearCaches()
				case "table_eviction":
					ctrl.EvictExactCounts("public", "orders")
				case "unrelated_eviction":
					ctrl.EvictExactCounts("public", "unrelated")
				case "all_eviction":
					ctrl.EvictAllExactCounts()
				case "result_close":
					tabs := ctrl.mgr.(*ui.ResultTabsHelper)
					origin := tabs.Active()
					require.NoError(t, tabs.OpenResultTab("replacement", nil))
					require.NoError(t, tabs.Close(origin))
				case "result_close_all":
					ctrl.mgr.(*ui.ResultTabsHelper).CloseAll()
				case "close", "close_reopen":
					require.NoError(t, registerAndDispatch(t, ctrl, commands.RelationshipPanelToggle))
					if invalidate == "close_reopen" {
						require.NoError(t, registerAndDispatch(t, ctrl, commands.RelationshipPanelToggle))
					}
				}
				unblock()
				select {
				case <-finished:
				case <-time.After(time.Second):
					t.Fatal("count lookup did not settle")
				}
				pump.drain(t)
				require.NotContains(t, ctrl.estimates, key, "late count may not repopulate invalidated cache")
				if invalidate == "table_eviction" || invalidate == "unrelated_eviction" {
					require.Equal(t, estimateResult{exact: 99, hasExact: true}, ctrl.estimates["unrelated_completed"])
				}
				require.NotContains(t, ctrl.ctx.Body(), "1187")
			})
		}
	}
}

func TestReview_relationship_count_selection_retains_valid_result(t *testing.T) {
	ctx, _ := newPanelContext(t)
	h := newTabWithRow(t, "public.people", []string{"id"}, []any{int64(7)})
	t.Cleanup(h.CloseAll)
	fks := []models.ForeignKey{
		{Schema: "public", Table: "messages", Columns: []string{"sender_id"}, RefColumns: []string{"id"}},
		{Schema: "public", Table: "messages", Columns: []string{"recipient_id"}, RefColumns: []string{"id"}},
	}
	pump := &reviewCountUI{}
	ctrl := NewRelationshipPanelController(testCommon(t), CoreDeps{}, ctx, &fakePanelTree{}, h, nil, fkList(fks...), pump.post)
	var jobs []func()
	ctrl.SetOnWorker(func(fn func()) { jobs = append(jobs, fn) })
	arrived, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	calls := 0
	ctrl.SetExactResolver(func(_ context.Context, fk models.ForeignKey, _ []any) (int64, error) {
		calls++
		if fk.Columns[0] == "sender_id" {
			close(arrived)
			<-release
			return 11, nil
		}
		return 22, nil
	})
	focusInbound(t, ctrl)
	require.Len(t, jobs, 1)
	job := jobs[0]
	go func() { defer close(finished); job() }()
	t.Cleanup(func() { unblock(); <-finished })
	select {
	case <-arrived:
	case <-time.After(time.Second):
		t.Fatal("sender count did not start")
	}
	require.NoError(t, registerAndDispatch(t, ctrl, commands.RelationshipPanelDown))
	require.Len(t, jobs, 2)
	unblock()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("sender count did not settle")
	}
	pump.drain(t)
	first, second := ctrl.inbound[0].estimateKey, ctrl.inbound[1].estimateKey
	require.Equal(t, estimateResult{exact: 11, hasExact: true}, ctrl.estimates[first])
	require.NotContains(t, ctrl.estimates, second)
	require.Equal(t, 1, ctrl.selIdx)
	jobs[1]()
	pump.drain(t)
	require.Equal(t, estimateResult{exact: 22, hasExact: true}, ctrl.estimates[second])
	require.NoError(t, registerAndDispatch(t, ctrl, commands.RelationshipPanelUp))
	require.Len(t, jobs, 2, "returning to sender uses its own completed cache")
	require.Equal(t, 2, calls)
}

func TestReview_relationship_count_single_ui_publication(t *testing.T) {
	ctrl, _ := inboundOnlyTabAndRev(t, 42)
	pump := &reviewCountUI{}
	ctrl.onUIContentOnly = pump.post
	var jobs []func()
	ctrl.SetOnWorker(func(fn func()) { jobs = append(jobs, fn) })
	ctrl.SetExactResolver(exactFn(1187, nil))
	focusInbound(t, ctrl)
	pump.drain(t)
	require.Len(t, jobs, 1)
	jobs[0]()
	pump.mu.Lock()
	publications := pump.jobs
	pump.jobs = nil
	pump.mu.Unlock()
	require.Len(t, publications, 1)
	require.NoError(t, publications[0]())
	require.Contains(t, ctrl.ctx.Body(), "1187", "render inside the guarded publication, not a later UI turn")
	pump.mu.Lock()
	queued := len(pump.jobs)
	pump.mu.Unlock()
	require.Zero(t, queued, "a delayed repaint would outlive the publication's tab guard")
	beforeClose := ctrl.ctx.Body()
	ctrl.mgr.(*ui.ResultTabsHelper).CloseAll()
	pump.drain(t)
	require.Equal(t, beforeClose, ctrl.ctx.Body(), "no queued count callback can repaint after tab removal")
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
