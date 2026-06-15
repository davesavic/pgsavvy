package data

import (
	"errors"
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// TestSnapshotInvalidation_StoreInvalidateSchema covers SchemaMetadataStore
// .InvalidateSchema: it drops every lazy (column+FK) entry for the target
// schema, leaves other schemas' lazy entries intact, and leaves the eager
// table-name list untouched (it self-heals on the next LoadEager).
func TestSnapshotInvalidation_StoreInvalidateSchema(t *testing.T) {
	s := NewSchemaMetadataStore()

	s.SetTables("public", sampleTableEntries())
	s.SetColumns("public", "users", sampleColumns())
	s.SetForeignKeys("public", "orders", sampleFKs())
	s.SetColumns("sales", "leads", sampleColumns())

	s.InvalidateSchema("public")

	_, ok := s.Columns("public", "users")
	require.False(t, ok, "public.users columns dropped")
	_, ok = s.ForeignKeys("public", "orders")
	require.False(t, ok, "public.orders FKs dropped")

	// Other schema's lazy entry survives.
	_, ok = s.Columns("sales", "leads")
	require.True(t, ok, "sales.leads columns survive a public invalidation")

	// Eager table-name list for the invalidated schema is left intact.
	require.Equal(t, []string{"users", "orders"}, s.TableNames("public"),
		"eager table names survive InvalidateSchema")
}

// TestSnapshotInvalidation_WarmerInvalidateSchema covers the warmer wrapper:
// it drops the store's lazy entries AND clears the per-key cooldown so a key
// that was in a post-failure cooldown re-warms immediately afterward.
func TestSnapshotInvalidation_WarmerInvalidateSchema(t *testing.T) {
	f := &fakeQuerier{columns: sampleColumns(), fks: sampleFKs()}
	// First call fails (opens a cooldown), later calls succeed.
	f.colsErr = errors.New("boom")
	w := NewSchemaWarmer(NewSchemaMetadataStore(), syncDeps(f), nil)
	clock := time.Now()
	w.now = func() time.Time { return clock }

	// Fail once -> cooldown opens for public.users.
	w.WarmTable("public", "users")
	require.Equal(t, int64(1), f.colCalls.Load())
	_, ok := w.Store().Columns("public", "users")
	require.False(t, ok, "failed warm leaves entry unloaded")

	// Within cooldown a re-warm is suppressed.
	w.WarmTable("public", "users")
	require.Equal(t, int64(1), f.colCalls.Load(), "suppressed during cooldown")

	// InvalidateSchema clears the cooldown; the driver now succeeds.
	f.colsErr = nil
	w.InvalidateSchema("public")
	w.WarmTable("public", "users")
	require.Equal(t, int64(2), f.colCalls.Load(), "cooldown cleared -> warm re-issued")
	cols, ok := w.Store().Columns("public", "users")
	require.True(t, ok, "re-warm landed")
	require.Len(t, cols, len(sampleColumns()))
}

// TestSnapshotInvalidation_WarmerInvalidateTable covers the single-table
// manual-'r' wrapper: it drops one (schema,table) lazy entry + its cooldown,
// leaving sibling tables in the same schema intact.
func TestSnapshotInvalidation_WarmerInvalidateTable(t *testing.T) {
	f := &fakeQuerier{columns: sampleColumns(), fks: sampleFKs()}
	w := NewSchemaWarmer(NewSchemaMetadataStore(), syncDeps(f), nil)

	w.WarmTable("public", "users")
	w.WarmTable("public", "orders")
	_, ok := w.Store().Columns("public", "users")
	require.True(t, ok)
	_, ok = w.Store().Columns("public", "orders")
	require.True(t, ok)

	w.InvalidateTable("public", "users")

	_, ok = w.Store().Columns("public", "users")
	require.False(t, ok, "users invalidated")
	_, ok = w.Store().Columns("public", "orders")
	require.True(t, ok, "orders (sibling) untouched")
}

// TestSnapshotInvalidation_WarmerReset covers reconnect teardown: Reset drops
// every store tier AND clears the cooldown state so a table that was in
// cooldown at disconnect is not suppressed on the next connection.
func TestSnapshotInvalidation_WarmerReset(t *testing.T) {
	f := &fakeQuerier{tables: sampleTables(), functions: []string{"now"}}
	f.colsErr = errors.New("boom") // first warm fails -> cooldown
	w := NewSchemaWarmer(NewSchemaMetadataStore(), syncDeps(f), nil)
	clock := time.Now()
	w.now = func() time.Time { return clock }

	// Populate eager + open a cooldown on a lazy key.
	w.LoadEager("public")
	w.WarmTable("public", "users")
	require.NotNil(t, w.Store().TableNames("public"))
	require.Equal(t, int64(1), f.colCalls.Load())

	// Reset: store cleared, cooldown cleared.
	w.Reset()
	require.Nil(t, w.Store().TableNames("public"), "eager tier cleared on Reset")
	require.Nil(t, w.Store().FunctionNames(), "function names cleared on Reset")

	// A re-warm for the previously-cooled key is allowed immediately (no
	// cooldown suppression), and now succeeds.
	f.colsErr = nil
	f.columns = sampleColumns()
	w.WarmTable("public", "users")
	require.Equal(t, int64(2), f.colCalls.Load(), "cooldown cleared by Reset -> warm re-issued at same clock")
	_, ok := w.Store().Columns("public", "users")
	require.True(t, ok)
}

// deferredWorkerDeps wires the warmer with a worker queue that DEFERS each warm
// body instead of running it inline, plus an inline UI publish. This lets a test
// hold a warm "in flight" (claimed, but its body not yet run) so an invalidation
// can be interleaved between claim and publish — the exact race the generation
// guard closes. Returns the deps and the channel of queued bodies to drain.
func deferredWorkerDeps(f *fakeQuerier) (warmDeps, chan func(gocui.Task) error) {
	workCh := make(chan func(gocui.Task) error, 4)
	return warmDeps{
		LoadTables:            f.LoadTables,
		LoadColumns:           f.LoadColumns,
		LoadForeignKeys:       f.LoadForeignKeys,
		LoadFunctions:         f.LoadFunctions,
		OnWorker:              func(fn func(gocui.Task) error) { workCh <- fn },
		OnUIThreadContentOnly: func(fn func() error) { _ = fn() },
	}, workCh
}

// TestSnapshotInvalidation_InvalidateTableDuringInflightWarmDropsStalePublish is
// the concurrency-AC regression: a DDL (InvalidateTable) completing while a warm
// for the SAME table is in flight must end with a consistent, non-stale entry.
// Without the generation guard the in-flight warm's late publish repopulated the
// store with pre-ALTER columns, so the next read saw Columns ok==true and never
// re-warmed. The guard DROPS the superseded publish, leaving the key UNLOADED so
// the next WarmTable re-loads fresh columns.
func TestSnapshotInvalidation_InvalidateTableDuringInflightWarmDropsStalePublish(t *testing.T) {
	staleCols := []models.Column{{Name: "stale_col"}}
	freshCols := []models.Column{{Name: "fresh_col"}}

	f := &fakeQuerier{columns: staleCols, fks: sampleFKs()}
	deps, workCh := deferredWorkerDeps(f)
	w := NewSchemaWarmer(NewSchemaMetadataStore(), deps, nil)

	// Start a warm: claim marks public.users in-flight; body is queued, NOT run.
	w.WarmTable("public", "users")
	require.Len(t, workCh, 1, "warm body queued in-flight")

	// DDL lands while the warm is in flight (loaded conceptually, not published).
	w.InvalidateTable("public", "users")

	// Release the publish: body runs, loads STALE columns, then publishes — the
	// generation guard must DROP it.
	body := <-workCh
	require.NoError(t, body(gocui.NewFakeTask()))
	require.Equal(t, int64(1), f.colCalls.Load(), "the in-flight warm did issue its load")

	// Store entry must be UNLOADED (not the stale data), so a read re-warms.
	_, ok := w.Store().Columns("public", "users")
	require.False(t, ok, "superseded warm's stale publish was dropped -> entry unloaded")

	// A subsequent warm DOES reload, and lands fresh post-ALTER columns.
	f.mu.Lock()
	f.columns = freshCols
	f.mu.Unlock()

	w.WarmTable("public", "users")
	require.Len(t, workCh, 1, "re-warm allowed: key is re-warmable, not stuck in-flight")
	body = <-workCh
	require.NoError(t, body(gocui.NewFakeTask()))
	require.Equal(t, int64(2), f.colCalls.Load(), "re-warm issued a fresh load")

	cols, ok := w.Store().Columns("public", "users")
	require.True(t, ok, "fresh warm landed")
	require.Equal(t, freshCols, cols, "store holds fresh post-ALTER columns, not stale")
}

// TestSnapshotInvalidation_InvalidateSchemaDuringInflightWarmDropsStalePublish
// is the schema-wide variant: a schema-level DDL invalidation (InvalidateSchema)
// landing while a same-table warm is in flight must likewise drop the stale
// publish and leave the key re-warmable.
func TestSnapshotInvalidation_InvalidateSchemaDuringInflightWarmDropsStalePublish(t *testing.T) {
	staleCols := []models.Column{{Name: "stale_col"}}

	f := &fakeQuerier{columns: staleCols, fks: sampleFKs()}
	deps, workCh := deferredWorkerDeps(f)
	w := NewSchemaWarmer(NewSchemaMetadataStore(), deps, nil)

	w.WarmTable("public", "users")
	require.Len(t, workCh, 1)

	w.InvalidateSchema("public")

	body := <-workCh
	require.NoError(t, body(gocui.NewFakeTask()))

	_, ok := w.Store().Columns("public", "users")
	require.False(t, ok, "schema invalidation mid-flight drops the stale publish")

	// Re-warmable afterward.
	w.WarmTable("public", "users")
	require.Len(t, workCh, 1, "key re-warmable after a dropped publish")
}

// TestSnapshotInvalidation_StoreInvalidateSchemaNUL guards the prefix match:
// a schema whose name is a prefix of another (e.g. "pub" vs "public") must not
// cross-invalidate, because the lazy key uses a NUL separator.
func TestSnapshotInvalidation_StoreInvalidateSchemaNUL(t *testing.T) {
	s := NewSchemaMetadataStore()
	s.SetColumns("pub", "t", []models.Column{{Name: "a"}})
	s.SetColumns("public", "t", []models.Column{{Name: "b"}})

	s.InvalidateSchema("pub")

	_, ok := s.Columns("pub", "t")
	require.False(t, ok, "pub.t dropped")
	_, ok = s.Columns("public", "t")
	require.True(t, ok, "public.t NOT dropped by a 'pub' invalidation")
}
