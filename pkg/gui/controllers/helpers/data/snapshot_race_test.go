package data

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/dbsavvy/pkg/gui/editor"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// serializedQuerier models the per-Session worker contract: every LoadX is
// funnelled through ONE mutex so the underlying "connection" is never touched
// concurrently. If completion ever bypassed the warmer and hit a raw session from
// the UI goroutine (the jxw regression), the race detector would flag the
// store/worker interleaving below — here every driver call is serialized, exactly
// as the real ConnectHelper.submit guarantees. tableCalls/colCalls let the test
// assert a session op actually ran in-flight.
type serializedQuerier struct {
	mu sync.Mutex // serializes all "connection" access (the pgx-conn stand-in)

	cols []models.Column

	colCalls   atomic.Int64
	tableCalls atomic.Int64

	// inflight is closed once the first serialized op starts, so the test can
	// guarantee completion fires WHILE a session operation holds the worker.
	inflightOnce sync.Once
	inflight     chan struct{}
}

func newSerializedQuerier() *serializedQuerier {
	return &serializedQuerier{
		cols:     []models.Column{{Name: "id"}, {Name: "name"}},
		inflight: make(chan struct{}),
	}
}

func (q *serializedQuerier) signalInflight() {
	q.inflightOnce.Do(func() { close(q.inflight) })
}

func (q *serializedQuerier) LoadTables(_ context.Context, _ string) ([]*models.Table, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.tableCalls.Add(1)
	q.signalInflight()
	return []*models.Table{{Name: "users", Schema: "public", Kind: "table"}}, nil
}

func (q *serializedQuerier) LoadColumns(_ context.Context, _, _ string) ([]models.Column, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.colCalls.Add(1)
	q.signalInflight()
	out := make([]models.Column, len(q.cols))
	copy(out, q.cols)
	return out, nil
}

func (q *serializedQuerier) LoadForeignKeys(_ context.Context, _, _ string) ([]models.ForeignKey, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return nil, nil
}

func (q *serializedQuerier) LoadFunctions(_ context.Context) ([]string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return []string{"now", "lower"}, nil
}

// TestSnapshotRace_CompletionDuringInflightSessionOp is the capstone race
// test (jxw criterion #2). It runs, concurrently and under -race:
//
//   - A REAL async worker goroutine pool draining warm bodies (OnWorker), each of
//     which performs a serialized "session op" (LoadColumns) and then publishes
//     into the store on the UI-drain goroutine.
//   - A dedicated UI-drain goroutine executing the OnUIThreadContentOnly publish
//     closures (store WRITES) — the gocui MainLoop stand-in.
//   - Many completion Suggest calls (store READS) firing from yet more goroutines
//     while the warm/session op is in flight.
//
// The store's RWMutex + the warmer's serialized driver path must make every
// READ (Suggest) safe against every WRITE (warm publish) with no data race on the
// store or the (serialized) "connection". A direct-session completion call from
// the UI goroutine — the jxw bug — would interleave with the worker's serialized
// op and the race detector would fire.
func TestSnapshotRace_CompletionDuringInflightSessionOp(t *testing.T) {
	q := newSerializedQuerier()
	store := NewSchemaMetadataStore()
	// Seed eager tables so Suggest has a table context to read while warms churn.
	store.SetTables("public", []TableEntry{
		{Name: "users", Kind: "table"},
		{Name: "orders", Kind: "table"},
	})

	// A real worker pool: OnWorker spawns a goroutine per warm body (tracked via
	// wg). A real UI-drain goroutine runs the publish closures off a buffered
	// channel. This separates the worker goroutine (driver op) from the UI
	// goroutine (store write), so direct cross-goroutine store access shows up as a
	// race.
	var wg sync.WaitGroup
	uiCh := make(chan func() error, 256)

	deps := warmDeps{
		LoadTables:      q.LoadTables,
		LoadColumns:     q.LoadColumns,
		LoadForeignKeys: q.LoadForeignKeys,
		LoadFunctions:   q.LoadFunctions,
		OnWorker: func(fn func(gocui.Task) error) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = fn(gocui.NewFakeTask())
			}()
		},
		OnUIThreadContentOnly: func(fn func() error) { uiCh <- fn },
	}
	w := NewSchemaWarmer(store, deps, nil)

	src := editor.NewSchemaSource(metaAdapter{store}, w, func() string { return "public" })

	// Dedicated UI-drain goroutine (the "MainLoop"): runs every publish closure on
	// ONE goroutine, distinct from the worker goroutines and the Suggest readers.
	stopUI := make(chan struct{})
	var uiWG sync.WaitGroup
	uiWG.Add(1)
	go func() {
		defer uiWG.Done()
		for {
			select {
			case fn := <-uiCh:
				_ = fn()
			case <-stopUI:
				for {
					select {
					case fn := <-uiCh:
						_ = fn()
					default:
						return
					}
				}
			}
		}
	}()

	suggest := func(line string) {
		b := &editor.Buffer{Lines: []editor.Line{{Runes: []rune(line)}}}
		pos := editor.Position{Line: 0, Col: len([]rune(line))}
		_ = src.Suggest(context.Background(), b, pos)
	}

	// Kick off an in-flight session op first: warm "orders" (its serialized
	// LoadColumns holds the worker), then ensure completion fires WHILE it runs.
	w.WarmTable("public", "orders")

	// Wait until a serialized op is actually in flight before hammering Suggest.
	select {
	case <-q.inflight:
	case <-time.After(2 * time.Second):
		t.Fatal("no serialized session op started")
	}

	// Now fan out: concurrent Suggest reads (table + column contexts) AND more
	// warm-triggering Suggests (column miss fires WarmTable -> more publishes),
	// all while the worker pool serializes driver ops and the UI goroutine writes.
	var readers sync.WaitGroup
	for i := 0; i < 40; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			suggest("SELECT * FROM ") // table read
			suggest("SELECT users.")  // column read -> warm on miss
			suggest("SELECT orders.")
		}()
	}
	readers.Wait()

	// Let every dispatched warm body finish, then drain remaining publishes.
	wg.Wait()
	close(stopUI)
	uiWG.Wait()

	// A serialized session op did run in-flight (sanity: the test exercised the
	// concurrency it claims to).
	require.Positive(t, q.colCalls.Load(), "at least one serialized column op ran")

	// And the store is consistent after the storm: warmed tables hold their columns.
	cols, ok := store.Columns("public", "users")
	require.True(t, ok, "users warmed to completion")
	require.Equal(t, []models.Column{{Name: "id"}, {Name: "name"}}, cols)
}

// TestSnapshotRace_SuggestReadsVsWarmPublishWrites is a tighter, higher-pressure
// variant focused purely on the store: N reader goroutines calling Suggest
// (store reads via the SchemaSource) race M warm publishers (store writes) for
// the SAME keys. The store's RWMutex + deep-copy reads must keep this race-clean.
func TestSnapshotRace_SuggestReadsVsWarmPublishWrites(t *testing.T) {
	store := NewSchemaMetadataStore()
	store.SetTables("public", []TableEntry{{Name: "users", Kind: "table"}})
	src := editor.NewSchemaSource(metaAdapter{store}, nil, func() string { return "public" })

	var wg sync.WaitGroup

	// Writers: continuously (re)publish columns + FKs for public.users.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				store.SetColumns("public", "users", []models.Column{{Name: "id"}, {Name: "name"}})
				store.SetForeignKeys("public", "users", nil)
			}
		}(i)
	}

	// Readers: Suggest hammers the store with table + column reads.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b := &editor.Buffer{Lines: []editor.Line{{Runes: []rune("SELECT users.")}}}
			pos := editor.Position{Line: 0, Col: len([]rune("SELECT users."))}
			for j := 0; j < 200; j++ {
				_ = src.Suggest(context.Background(), b, pos)
			}
		}()
	}

	wg.Wait()
}
