package data

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// fakeQuerier is a synchronous, race-safe stand-in for the serialized
// ConnectHelper wrappers. Each LoadX increments a counter and returns the
// configured payload or error. It models the "one query per call" contract
// without a live Session.
type fakeQuerier struct {
	mu sync.Mutex

	tables    []*models.Table
	tablesErr error
	columns   []models.Column
	colsErr   error
	fks       []models.ForeignKey
	fksErr    error
	functions []string
	fnErr     error

	tableCalls atomic.Int64
	colCalls   atomic.Int64
	fkCalls    atomic.Int64
	fnCalls    atomic.Int64
}

func (f *fakeQuerier) LoadTables(_ context.Context, _ string) ([]*models.Table, error) {
	f.tableCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tables, f.tablesErr
}

func (f *fakeQuerier) LoadColumns(_ context.Context, _, _ string) ([]models.Column, error) {
	f.colCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.columns, f.colsErr
}

func (f *fakeQuerier) LoadForeignKeys(_ context.Context, _, _ string) ([]models.ForeignKey, error) {
	f.fkCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fks, f.fksErr
}

func (f *fakeQuerier) LoadFunctions(_ context.Context) ([]string, error) {
	f.fnCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.functions, f.fnErr
}

// syncDeps wires the warmer's seam to the fake querier with SYNCHRONOUS
// OnWorker/OnUIThreadContentOnly: both run their closure inline on the calling
// goroutine, making the asynchronous warm fully deterministic in tests while
// still exercising the real serialized-then-publish control flow.
func syncDeps(f *fakeQuerier) warmDeps {
	return warmDeps{
		LoadTables:      f.LoadTables,
		LoadColumns:     f.LoadColumns,
		LoadForeignKeys: f.LoadForeignKeys,
		LoadFunctions:   f.LoadFunctions,
		OnWorker:        func(fn func(gocui.Task) error) { _ = fn(gocui.NewFakeTask()) },
		OnUIThreadContentOnly: func(fn func() error) {
			_ = fn()
		},
	}
}

func sampleTables() []*models.Table {
	return []*models.Table{
		{Name: "orders", Schema: "public", Kind: "table"},
		{Name: "users", Schema: "public", Kind: "view"},
	}
}

func TestSchemaWarmEagerLoadsTablesThenFunctions(t *testing.T) {
	f := &fakeQuerier{tables: sampleTables(), functions: []string{"now", "lower"}}
	w := NewSchemaWarmer(NewSchemaMetadataStore(), syncDeps(f), nil)

	w.LoadEager("public")

	require.Equal(t, int64(1), f.tableCalls.Load(), "exactly one LoadTables")
	require.Equal(t, int64(1), f.fnCalls.Load(), "exactly one LoadFunctions")
	require.Equal(t, []string{"orders", "users"}, w.Store().TableNames("public"))
	require.Equal(t, []string{"now", "lower"}, w.Store().FunctionNames())
	// Kind is threaded through the eager projection.
	require.Equal(t, "table", w.Store().TableKind("public", "orders"))
	require.Equal(t, "view", w.Store().TableKind("public", "users"))
}

func TestSchemaWarmEagerEmptySchemaStoresEmptyNoWarm(t *testing.T) {
	f := &fakeQuerier{tables: nil, functions: nil}
	w := NewSchemaWarmer(NewSchemaMetadataStore(), syncDeps(f), nil)

	w.LoadEager("public")

	// Empty (non-nil) table-name list is stored -> read is distinguishable
	// from "never loaded".
	names := w.Store().TableNames("public")
	require.NotNil(t, names)
	require.Empty(t, names)
	require.Equal(t, int64(0), f.colCalls.Load(), "empty schema fires no warm")
	require.Equal(t, int64(0), f.fkCalls.Load())
}

func TestSchemaWarmEagerTableFailureStillLoadsFunctions(t *testing.T) {
	f := &fakeQuerier{tablesErr: errors.New("boom"), functions: []string{"now"}}
	w := NewSchemaWarmer(NewSchemaMetadataStore(), syncDeps(f), nil)

	w.LoadEager("public")

	require.Nil(t, w.Store().TableNames("public"), "failed table load -> unloaded")
	require.Equal(t, []string{"now"}, w.Store().FunctionNames(), "functions still load")
}

func TestSchemaWarmEagerFailureRetries(t *testing.T) {
	f := &fakeQuerier{tablesErr: errors.New("transient")}
	w := NewSchemaWarmer(NewSchemaMetadataStore(), syncDeps(f), nil)

	w.LoadEager("public")
	require.Nil(t, w.Store().TableNames("public"))

	// Recover and retry: a fresh LoadEager re-issues the query.
	f.mu.Lock()
	f.tablesErr = nil
	f.tables = sampleTables()
	f.mu.Unlock()

	w.LoadEager("public")
	require.Equal(t, []string{"orders", "users"}, w.Store().TableNames("public"))
	require.Equal(t, int64(2), f.tableCalls.Load(), "retry re-issued the load")
}

func TestSchemaWarmTablePopulatesStoreAndFiresOnWarmedOnce(t *testing.T) {
	f := &fakeQuerier{columns: sampleColumns(), fks: sampleFKs()}
	w := NewSchemaWarmer(NewSchemaMetadataStore(), syncDeps(f), nil)

	var warmedCalls int
	var warmedSchema, warmedTable string
	w.SetOnWarmed(func(schema, table string) {
		warmedCalls++
		warmedSchema, warmedTable = schema, table
	})

	w.WarmTable("public", "orders")

	cols, ok := w.Store().Columns("public", "orders")
	require.True(t, ok)
	require.Equal(t, sampleColumns(), cols)
	fks, ok := w.Store().ForeignKeys("public", "orders")
	require.True(t, ok)
	require.Equal(t, sampleFKs(), fks)

	require.Equal(t, 1, warmedCalls, "onWarmed fires exactly once")
	require.Equal(t, "public", warmedSchema)
	require.Equal(t, "orders", warmedTable)
	require.Equal(t, int64(1), f.colCalls.Load())
	require.Equal(t, int64(1), f.fkCalls.Load())
}

func TestSchemaWarmTableAlreadyLoadedIsNoOp(t *testing.T) {
	f := &fakeQuerier{columns: sampleColumns(), fks: sampleFKs()}
	w := NewSchemaWarmer(NewSchemaMetadataStore(), syncDeps(f), nil)

	w.WarmTable("public", "orders")
	require.Equal(t, int64(1), f.colCalls.Load())

	// Second warm: store already has columns -> no driver call.
	w.WarmTable("public", "orders")
	require.Equal(t, int64(1), f.colCalls.Load(), "loaded key -> no duplicate call")
	require.Equal(t, int64(1), f.fkCalls.Load())
}

func TestSchemaWarmTableColumnsErrorLeavesEntryUnloaded(t *testing.T) {
	f := &fakeQuerier{colsErr: errors.New("permission denied")}
	w := NewSchemaWarmer(NewSchemaMetadataStore(), syncDeps(f), nil)

	var gotErr error
	w.SetOnWarmError(func(_, _ string, err error) { gotErr = err })

	w.WarmTable("public", "orders")

	_, ok := w.Store().Columns("public", "orders")
	require.False(t, ok, "failed columns load -> entry stays unloaded (retryable)")
	require.Error(t, gotErr, "onWarmError surfaces the failure")
	require.Equal(t, int64(0), f.fkCalls.Load(), "FK load skipped after column error")
}

func TestSchemaWarmTableFKErrorLeavesEntryUnloaded(t *testing.T) {
	f := &fakeQuerier{columns: sampleColumns(), fksErr: errors.New("boom")}
	w := NewSchemaWarmer(NewSchemaMetadataStore(), syncDeps(f), nil)

	w.WarmTable("public", "orders")

	_, ok := w.Store().Columns("public", "orders")
	require.False(t, ok, "partial success must not publish a half-loaded entry")
}

func TestSchemaWarmTableFailureCooldownSuppressesRetry(t *testing.T) {
	f := &fakeQuerier{colsErr: errors.New("transient")}
	w := NewSchemaWarmer(NewSchemaMetadataStore(), syncDeps(f), nil)

	clock := time.Unix(0, 0)
	w.now = func() time.Time { return clock }

	w.WarmTable("public", "orders")
	require.Equal(t, int64(1), f.colCalls.Load())

	// Within cooldown -> suppressed.
	w.WarmTable("public", "orders")
	require.Equal(t, int64(1), f.colCalls.Load(), "warm suppressed during cooldown")

	// Advance past cooldown -> retry allowed.
	clock = clock.Add(warmFailureCooldown + time.Second)
	f.mu.Lock()
	f.colsErr = nil
	f.columns = sampleColumns()
	f.mu.Unlock()

	w.WarmTable("public", "orders")
	require.Equal(t, int64(2), f.colCalls.Load(), "retry allowed after cooldown")
	_, ok := w.Store().Columns("public", "orders")
	require.True(t, ok, "retry succeeds and populates store")
}

// chanDeps defers OnWorker closures onto a channel so the test controls when the
// worker body runs — used to prove an IN-FLIGHT (not yet completed) warm
// suppresses a concurrent duplicate.
func TestSchemaWarmTableInflightSuppressesDuplicate(t *testing.T) {
	f := &fakeQuerier{columns: sampleColumns(), fks: sampleFKs()}
	store := NewSchemaMetadataStore()

	workCh := make(chan func(gocui.Task) error, 4)
	deps := warmDeps{
		LoadTables:            f.LoadTables,
		LoadColumns:           f.LoadColumns,
		LoadForeignKeys:       f.LoadForeignKeys,
		LoadFunctions:         f.LoadFunctions,
		OnWorker:              func(fn func(gocui.Task) error) { workCh <- fn },
		OnUIThreadContentOnly: func(fn func() error) { _ = fn() },
	}
	w := NewSchemaWarmer(store, deps, nil)

	// First warm: claimed in-flight, body queued but NOT yet run.
	w.WarmTable("public", "orders")
	require.Len(t, workCh, 1)

	// Second warm while first is in-flight: must not enqueue a second body.
	w.WarmTable("public", "orders")
	require.Len(t, workCh, 1, "in-flight key -> no duplicate worker dispatch")

	// Drain and run the single queued body.
	fn := <-workCh
	require.NoError(t, fn(gocui.NewFakeTask()))
	require.Equal(t, int64(1), f.colCalls.Load(), "exactly one column round-trip")

	_, ok := store.Columns("public", "orders")
	require.True(t, ok)
}

// TestSchemaWarmTableConcurrentDoubleSubmit drives many concurrent WarmTable
// calls for the same key through the synchronous deps to catch races and prove
// at most one round-trip. Run under -race.
func TestSchemaWarmTableConcurrentDoubleSubmit(t *testing.T) {
	f := &fakeQuerier{columns: sampleColumns(), fks: sampleFKs()}
	w := NewSchemaWarmer(NewSchemaMetadataStore(), syncDeps(f), nil)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.WarmTable("public", "orders")
		}()
	}
	wg.Wait()

	require.Equal(t, int64(1), f.colCalls.Load(), "at most one column round-trip")
	require.Equal(t, int64(1), f.fkCalls.Load(), "at most one FK round-trip")
	_, ok := w.Store().Columns("public", "orders")
	require.True(t, ok)
}
