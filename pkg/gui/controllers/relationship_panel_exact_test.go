package controllers

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestReview_relationship_count_foreign_keys(t *testing.T) {
	for _, composite := range []bool{false, true} {
		for _, named := range []bool{false, true} {
			t.Run(fmt.Sprintf("composite=%t/named=%t", composite, named), func(t *testing.T) {
				ctx, _ := newPanelContext(t)
				h := newTabWithRow(t, "public.people", []string{"code", "tenant"}, []any{int64(7), int64(9)})
				t.Cleanup(h.CloseAll)
				fks := []models.ForeignKey{
					{Schema: "public", Table: "messages", Columns: []string{"sender_code"}, RefSchema: "public", RefTable: "people", RefColumns: []string{"code"}},
					{Schema: "public", Table: "messages", Columns: []string{"recipient_code"}, RefSchema: "public", RefTable: "people", RefColumns: []string{"code"}},
				}
				wantArgs := []any{int64(7)}
				if composite {
					fks[0].Columns = []string{"sender_tenant", "sender_code"}
					fks[1].Columns = []string{"recipient_tenant", "recipient_code"}
					for i := range fks {
						fks[i].RefColumns = []string{"tenant", "code"}
					}
					wantArgs = []any{int64(9), int64(7)}
				}
				if named {
					fks[0].Name, fks[1].Name = "fk_sender", "fk_recipient"
				}
				ctrl := NewRelationshipPanelController(testCommon(t), CoreDeps{}, ctx, &fakePanelTree{}, h, nil, fkList(fks...), nil)
				estimateCalls, exactCalls := [2]int{}, [2]int{}
				index := func(fk models.ForeignKey, args []any) int {
					require.Equal(t, wantArgs, args)
					if strings.HasPrefix(fk.Columns[0], "sender_") {
						require.Equal(t, fks[0], fk)
						return 0
					}
					require.Equal(t, fks[1], fk)
					return 1
				}
				ctrl.SetEstimateResolver(func(_ context.Context, fk models.ForeignKey, args []any) (int64, error) {
					i := index(fk, args)
					estimateCalls[i]++
					return int64((i + 1) * 100), nil
				})
				ctrl.SetExactResolver(func(_ context.Context, fk models.ForeignKey, args []any) (int64, error) {
					i := index(fk, args)
					exactCalls[i]++
					return int64((i + 1) * 11), nil
				})
				require.NoError(t, registerAndDispatch(t, ctrl, commands.RelationshipPanelToggle))
				require.Len(t, ctrl.inbound, 2)
				keys := []string{ctrl.inbound[0].estimateKey, ctrl.inbound[1].estimateKey}
				assert.NotEqual(t, keys[0], keys[1])
				assert.Equal(t, [2]int{1, 1}, estimateCalls)
				for i, key := range keys {
					assert.Equal(t, int64((i+1)*100), ctrl.estimates[key].rows)
				}
				require.NoError(t, registerAndDispatch(t, ctrl, commands.RelationshipPanelEnter))
				assert.False(t, ctrl.estimates[keys[1]].hasExact, "sender count must not resolve recipient")
				require.NoError(t, registerAndDispatch(t, ctrl, commands.RelationshipPanelDown))
				for i, key := range keys {
					assert.Equal(t, estimateResult{rows: int64((i + 1) * 100), exact: int64((i + 1) * 11), hasExact: true}, ctrl.estimates[key])
					assert.Contains(t, ctx.Body(), fmt.Sprintf("%d", (i+1)*11))
				}
				require.NoError(t, registerAndDispatch(t, ctrl, commands.RelationshipPanelUp))
				require.NoError(t, registerAndDispatch(t, ctrl, commands.RelationshipPanelDown))
				assert.Equal(t, [2]int{1, 1}, exactCalls, "only the same relationship may hit the cache")
				ctrl.EvictExactCounts("public", "unrelated")
				require.NoError(t, registerAndDispatch(t, ctrl, commands.RelationshipPanelUp))
				assert.Equal(t, [2]int{1, 1}, exactCalls)
				ctrl.EvictExactCounts("public", "messages")
				require.NoError(t, registerAndDispatch(t, ctrl, commands.RelationshipPanelDown))
				require.NoError(t, registerAndDispatch(t, ctrl, commands.RelationshipPanelUp))
				assert.Equal(t, [2]int{2, 2}, exactCalls, "table eviction must invalidate both foreign keys")
			})
		}
	}
}

func TestReview_relationship_count_identity_parts(t *testing.T) {
	for _, part := range []string{"connection", "constraint", "child_schema", "child_table", "child_columns", "parent_schema", "parent_table", "parent_columns", "column_delimiters", "value_delimiters", "value_type"} {
		t.Run(part, func(t *testing.T) {
			ctx, _ := newPanelContext(t)
			cols, values := []string{"a", "b"}, []any{int64(7), int64(7)}
			if part == "value_delimiters" {
				values = []any{"a,b", "c"}
			}
			h := newTabWithRow(t, "public.people", cols, values)
			t.Cleanup(h.CloseAll)
			fk := models.ForeignKey{Name: "fk", Schema: "public", Table: "messages", Columns: []string{"x", "y"}, RefSchema: "public", RefTable: "people", RefColumns: []string{"a", "b"}}
			if part == "column_delimiters" {
				fk.Columns = []string{"x,y", "z"}
			}
			ctrl := NewRelationshipPanelController(testCommon(t), CoreDeps{}, ctx, &fakePanelTree{}, h, nil,
				func(context.Context, string, string) ([]models.ForeignKey, error) {
					return []models.ForeignKey{fk}, nil
				}, nil)
			calls := 0
			ctrl.SetExactResolver(func(context.Context, models.ForeignKey, []any) (int64, error) { calls++; return int64(calls * 11), nil })
			focusInbound(t, ctrl)
			first := ctrl.inbound[0].estimateKey
			switch part {
			case "connection":
				_, ri := h.Active().Identity()
				h.Active().SetIdentity("conn2", ri)
			case "constraint":
				fk.Name = "other_fk"
			case "child_schema":
				fk.Schema = "other"
			case "child_table":
				fk.Table = "other"
			case "child_columns":
				fk.Columns = []string{"y", "x"}
			case "parent_schema":
				fk.RefSchema = "other"
			case "parent_table":
				fk.RefTable = "other"
			case "parent_columns":
				fk.RefColumns = []string{"b", "a"}
			case "column_delimiters":
				fk.Columns = []string{"x", "y,z"}
			case "value_delimiters":
				h.Active().Grid().SetColumns(h.Active().Grid().Columns())
				h.Active().Grid().AppendRows([]models.Row{{Values: []any{"a", "b,c"}}})
			case "value_type":
				h.Active().Grid().SetColumns(h.Active().Grid().Columns())
				h.Active().Grid().AppendRows([]models.Row{{Values: []any{"7", int64(7)}}})
			}
			ctrl.NotifyActiveTabChanged()
			require.NoError(t, registerAndDispatch(t, ctrl, commands.RelationshipPanelExit))
			require.NoError(t, registerAndDispatch(t, ctrl, commands.RelationshipPanelEnter))
			second := ctrl.inbound[0].estimateKey
			assert.NotEqual(t, first, second)
			assert.Equal(t, 2, calls, "a different predicate identity must count separately")
			assert.EqualValues(t, 11, ctrl.estimates[first].exact)
			assert.EqualValues(t, 22, ctrl.estimates[second].exact)
			require.NoError(t, registerAndDispatch(t, ctrl, commands.RelationshipPanelExit))
			require.NoError(t, registerAndDispatch(t, ctrl, commands.RelationshipPanelEnter))
			assert.Equal(t, 2, calls)
			if part == "connection" {
				ctrl.EvictExactCounts("public", "messages")
				require.Empty(t, ctrl.estimates, "table eviction covers all connection identities")
			}
		})
	}
}

type reviewCountEncodingError int

func (reviewCountEncodingError) GobEncode() ([]byte, error) {
	return nil, errors.New("synthetic count-key encoding failure")
}

type reviewCountMap map[string]int

func (reviewCountMap) GobEncode() ([]byte, error) { return []byte("same"), nil }

type reviewCountChannel chan int

func (reviewCountChannel) GobEncode() ([]byte, error) { return []byte("same"), nil }

type reviewCountFunc func() int

func (reviewCountFunc) GobEncode() ([]byte, error) { return []byte("same"), nil }

func TestReview_relationship_count_native_values(t *testing.T) {
	for _, tc := range []struct {
		name      string
		values    func() []any
		other     []any
		cacheable bool
	}{
		{"integer_width", func() []any { return []any{int32(7)} }, []any{int64(7)}, true},
		{"bytes", func() []any { return []any{[]byte{0, ',', 255}} }, []any{[]byte{0, '|', 255}}, true},
		{"numeric", func() []any { return []any{pgtype.Numeric{Int: big.NewInt(123), Exp: -2, Valid: true}} }, []any{pgtype.Numeric{Int: big.NewInt(124), Exp: -2, Valid: true}}, true},
		{"uuid", func() []any { return []any{uuid.MustParse("00000000-0000-0000-0000-000000000001")} }, []any{uuid.MustParse("00000000-0000-0000-0000-000000000002")}, true},
		{"time", func() []any { return []any{time.Unix(123, 456).UTC()} }, []any{time.Unix(123, 457).UTC()}, true},
		{"infinity", func() []any { return []any{math.Inf(1)} }, []any{math.Inf(-1)}, true},
		{"nan", func() []any { return []any{math.Float64frombits(0x7ff8000000000001)} }, []any{math.Float64frombits(0x7ff8000000000002)}, true},
		{"nested_types", func() []any { return []any{[]any{int32(7), "a,b"}} }, []any{[]any{int64(7), "a,b"}}, true},
		{"nested_null", func() []any { return []any{[][]byte{nil}} }, []any{[][]byte{{}}}, false},
		{"map", func() []any { return []any{map[string]any{"value": 7}} }, []any{map[string]any{"value": 8}}, false},
		{"unencodable", func() []any { return []any{struct{ private int }{7}} }, []any{struct{ private int }{8}}, false},
		{"private_state", func() []any { return []any{struct{ Public, private int }{1, 7}} }, []any{struct{ Public, private int }{1, 8}}, false},
		{"nil_pointer", func() []any { return []any{(*int)(nil)} }, []any{new(int)}, false},
		{"encoding_error", func() []any { return []any{reviewCountEncodingError(7)} }, []any{reviewCountEncodingError(8)}, false},
		{"map_encoder", func() []any { return []any{reviewCountMap{"value": 7}} }, []any{reviewCountMap{"value": 8}}, false},
		{"pointer_map_encoder", func() []any { v := reviewCountMap{"value": 7}; return []any{&v} }, []any{&reviewCountMap{"value": 8}}, false},
		{"channel_encoder", func() []any { return []any{make(reviewCountChannel)} }, []any{make(reviewCountChannel)}, false},
		{"function_encoder", func() []any { return []any{reviewCountFunc(func() int { return 7 })} }, []any{reviewCountFunc(func() int { return 8 })}, false},
		{"depth", func() []any {
			var value any = int64(7)
			for range 18 {
				value = []any{value}
			}
			return []any{value}
		}, []any{int64(8)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _ := newPanelContext(t)
			h := newTabWithRow(t, "public.people", []string{"id"}, tc.values())
			t.Cleanup(h.CloseAll)
			fk := models.ForeignKey{Schema: "public", Table: "messages", Columns: []string{"person_id"}, RefSchema: "public", RefTable: "people", RefColumns: []string{"id"}}
			ctrl := NewRelationshipPanelController(testCommon(t), CoreDeps{}, ctx, &fakePanelTree{}, h, nil, fkList(fk), nil)
			calls := 0
			ctrl.SetExactResolver(func(context.Context, models.ForeignKey, []any) (int64, error) { calls++; return int64(calls * 11), nil })
			focusInbound(t, ctrl)
			require.Equal(t, 1, calls, "unsupported cache values must remain queryable")
			first := ctrl.inbound[0].estimateKey
			revisit := func(values []any) string {
				grid := h.Active().Grid()
				grid.SetColumns(grid.Columns())
				grid.AppendRows([]models.Row{{Values: values}})
				ctrl.NotifyActiveTabChanged()
				require.NoError(t, registerAndDispatch(t, ctrl, commands.RelationshipPanelExit))
				require.NoError(t, registerAndDispatch(t, ctrl, commands.RelationshipPanelEnter))
				return ctrl.inbound[0].estimateKey
			}
			same := revisit(tc.values())
			wantCalls := 1
			if tc.cacheable {
				require.Equal(t, first, same)
			} else {
				require.NotEqual(t, first, same)
				wantCalls++
			}
			require.Equal(t, wantCalls, calls)
			other := revisit(tc.other)
			require.NotEqual(t, first, other)
			require.NotEqual(t, same, other)
			require.Equal(t, wantCalls+1, calls)
			require.EqualValues(t, (wantCalls+1)*11, ctrl.estimates[other].exact)
		})
	}
}

func TestReview_relationship_count_quoted_table_eviction(t *testing.T) {
	ctx, _ := newPanelContext(t)
	h := newTabWithRow(t, "public.people", []string{"id"}, []any{int64(7)})
	t.Cleanup(h.CloseAll)
	fks := []models.ForeignKey{
		{Schema: "a.b", Table: "c", Columns: []string{"id"}, RefSchema: "public", RefTable: "people", RefColumns: []string{"id"}},
		{Schema: "a", Table: "b.c", Columns: []string{"id"}, RefSchema: "public", RefTable: "people", RefColumns: []string{"id"}},
	}
	ctrl := NewRelationshipPanelController(testCommon(t), CoreDeps{}, ctx, &fakePanelTree{}, h, nil, fkList(fks...), nil)
	ctrl.SetExactResolver(exactFn(11, nil))
	focusInbound(t, ctrl)
	require.NoError(t, registerAndDispatch(t, ctrl, commands.RelationshipPanelDown))
	first, second := ctrl.inbound[0].estimateKey, ctrl.inbound[1].estimateKey
	require.NotEqual(t, first, second)
	ctrl.EvictExactCounts("a.b", "c")
	require.NotContains(t, ctrl.estimates, first)
	require.Contains(t, ctrl.estimates, second)
	ctrl.EvictExactCounts("a", "b.c")
	require.Empty(t, ctrl.estimates)
}
