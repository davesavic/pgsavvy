package session_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/session"
)

// loaderFn is a per-test FKLoader that counts calls per key. It is the only
// dependency injected into FKCache: tests can stage results / errors without
// involving drivers.Session.
type loaderFn struct {
	mu        sync.Mutex
	calls     map[string]int
	results   map[string][]models.ForeignKey
	errors    map[string]error
	totalCall atomic.Int32
}

func newLoaderFn() *loaderFn {
	return &loaderFn{
		calls:   map[string]int{},
		results: map[string][]models.ForeignKey{},
		errors:  map[string]error{},
	}
}

func (l *loaderFn) keyOf(schema, table string) string { return schema + "." + table }

func (l *loaderFn) stage(schema, table string, fks []models.ForeignKey) {
	l.results[l.keyOf(schema, table)] = fks
}

func (l *loaderFn) stageErr(schema, table string, err error) {
	l.errors[l.keyOf(schema, table)] = err
}

func (l *loaderFn) callCount(schema, table string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls[l.keyOf(schema, table)]
}

func (l *loaderFn) load(_ context.Context, schema, table string) ([]models.ForeignKey, error) {
	k := l.keyOf(schema, table)
	l.mu.Lock()
	l.calls[k]++
	l.mu.Unlock()
	l.totalCall.Add(1)
	if err, ok := l.errors[k]; ok {
		return nil, err
	}
	return l.results[k], nil
}

func TestFKCache_GetMissTriggersLoaderOnce(t *testing.T) {
	t.Parallel()
	l := newLoaderFn()
	fk := models.ForeignKey{Name: "fk_a", Schema: "app", Table: "orders"}
	l.stage("app", "orders", []models.ForeignKey{fk})
	c := session.NewFKCache(l.load)

	got, err := c.Get(context.Background(), "app", "orders")
	if err != nil {
		t.Fatalf("Get miss: %v", err)
	}
	if len(got) != 1 || got[0].Name != "fk_a" {
		t.Fatalf("Get miss result = %+v, want one fk_a entry", got)
	}
	if calls := l.callCount("app", "orders"); calls != 1 {
		t.Errorf("loader calls after first Get = %d, want 1", calls)
	}
}

func TestFKCache_GetHitSkipsLoader(t *testing.T) {
	t.Parallel()
	l := newLoaderFn()
	l.stage("app", "orders", []models.ForeignKey{{Name: "fk_a"}})
	c := session.NewFKCache(l.load)

	if _, err := c.Get(context.Background(), "app", "orders"); err != nil {
		t.Fatalf("Get warm: %v", err)
	}
	if _, err := c.Get(context.Background(), "app", "orders"); err != nil {
		t.Fatalf("Get hit: %v", err)
	}
	if calls := l.callCount("app", "orders"); calls != 1 {
		t.Errorf("loader calls after cache hit = %d, want 1", calls)
	}
}

func TestFKCache_GetDifferentKeysAreIndependent(t *testing.T) {
	t.Parallel()
	l := newLoaderFn()
	l.stage("app", "orders", []models.ForeignKey{{Name: "fk_orders"}})
	l.stage("app", "items", []models.ForeignKey{{Name: "fk_items"}})
	c := session.NewFKCache(l.load)

	if _, err := c.Get(context.Background(), "app", "orders"); err != nil {
		t.Fatalf("Get orders: %v", err)
	}
	if _, err := c.Get(context.Background(), "app", "items"); err != nil {
		t.Fatalf("Get items: %v", err)
	}
	if l.callCount("app", "orders") != 1 || l.callCount("app", "items") != 1 {
		t.Errorf("loader calls = orders:%d items:%d, want 1/1",
			l.callCount("app", "orders"), l.callCount("app", "items"))
	}
}

func TestFKCache_InvalidateOnlyDropsMatchingKey(t *testing.T) {
	t.Parallel()
	l := newLoaderFn()
	l.stage("app", "orders", []models.ForeignKey{{Name: "fk_orders"}})
	l.stage("app", "items", []models.ForeignKey{{Name: "fk_items"}})
	c := session.NewFKCache(l.load)
	ctx := context.Background()

	_, _ = c.Get(ctx, "app", "orders")
	_, _ = c.Get(ctx, "app", "items")
	c.Invalidate("app", "orders")

	_, _ = c.Get(ctx, "app", "orders")
	_, _ = c.Get(ctx, "app", "items")

	if got := l.callCount("app", "orders"); got != 2 {
		t.Errorf("orders loader calls = %d, want 2 (initial + post-invalidate)", got)
	}
	if got := l.callCount("app", "items"); got != 1 {
		t.Errorf("items loader calls = %d, want 1 (still cached)", got)
	}
}

func TestFKCache_InvalidateAllClearsEverything(t *testing.T) {
	t.Parallel()
	l := newLoaderFn()
	l.stage("app", "orders", []models.ForeignKey{{Name: "fk_orders"}})
	l.stage("app", "items", []models.ForeignKey{{Name: "fk_items"}})
	c := session.NewFKCache(l.load)
	ctx := context.Background()

	_, _ = c.Get(ctx, "app", "orders")
	_, _ = c.Get(ctx, "app", "items")
	c.InvalidateAll()
	_, _ = c.Get(ctx, "app", "orders")
	_, _ = c.Get(ctx, "app", "items")

	if got := l.callCount("app", "orders"); got != 2 {
		t.Errorf("orders loader calls = %d, want 2", got)
	}
	if got := l.callCount("app", "items"); got != 2 {
		t.Errorf("items loader calls = %d, want 2", got)
	}
}

func TestFKCache_InvalidateAllOnEmptyIsNoop(t *testing.T) {
	t.Parallel()
	c := session.NewFKCache(func(context.Context, string, string) ([]models.ForeignKey, error) {
		return nil, nil
	})
	c.InvalidateAll()               // empty cache
	c.Invalidate("nope", "nothing") // absent key
	c.InvalidateAll()               // still empty
}

func TestFKCache_LoaderErrorIsNotCached(t *testing.T) {
	t.Parallel()
	l := newLoaderFn()
	sentinel := errors.New("transient driver failure")
	l.stageErr("app", "orders", sentinel)
	c := session.NewFKCache(l.load)
	ctx := context.Background()

	if _, err := c.Get(ctx, "app", "orders"); !errors.Is(err, sentinel) {
		t.Fatalf("first Get err = %v, want %v", err, sentinel)
	}
	// Recover: clear the error, stage a real result.
	delete(l.errors, l.keyOf("app", "orders"))
	l.stage("app", "orders", []models.ForeignKey{{Name: "fk_a"}})

	got, err := c.Get(ctx, "app", "orders")
	if err != nil {
		t.Fatalf("second Get err = %v, want nil (error should not be cached)", err)
	}
	if len(got) != 1 || got[0].Name != "fk_a" {
		t.Errorf("second Get result = %+v, want one fk_a entry", got)
	}
	if calls := l.callCount("app", "orders"); calls != 2 {
		t.Errorf("loader calls = %d, want 2 (no error caching)", calls)
	}
}

func TestFKCache_EmptyResultIsCachedAsNonNilSlice(t *testing.T) {
	t.Parallel()
	l := newLoaderFn()
	// Loader returns nil (no rows). Cache must store a non-nil empty slice
	// AND not re-invoke the loader on the next Get.
	c := session.NewFKCache(l.load)
	ctx := context.Background()

	got, err := c.Get(ctx, "app", "orders")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Errorf("Get result is nil; want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("Get result len = %d, want 0", len(got))
	}

	if _, err := c.Get(ctx, "app", "orders"); err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if calls := l.callCount("app", "orders"); calls != 1 {
		t.Errorf("loader calls = %d, want 1 (empty result should be cached)", calls)
	}
}

func TestFKCache_ConcurrentGetsAreSafe(t *testing.T) {
	t.Parallel()
	l := newLoaderFn()
	l.stage("app", "orders", []models.ForeignKey{{Name: "fk_a"}})
	l.stage("app", "items", []models.ForeignKey{{Name: "fk_b"}})
	c := session.NewFKCache(l.load)

	const workers = 32
	const iters = 100

	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		i := i
		go func() {
			defer wg.Done()
			ctx := context.Background()
			for j := 0; j < iters; j++ {
				schema, table := "app", "orders"
				if (i+j)%2 == 0 {
					table = "items"
				}
				if _, err := c.Get(ctx, schema, table); err != nil {
					t.Errorf("Get: %v", err)
					return
				}
				if j%17 == 0 {
					c.Invalidate(schema, table)
				}
				if j%53 == 0 {
					c.InvalidateAll()
				}
			}
		}()
	}
	wg.Wait()
	// We do not assert a specific loader call count — invalidations make it
	// nondeterministic. The point of this test is to drive -race.
	if total := l.totalCall.Load(); total < 2 {
		t.Errorf("total loader calls = %d, want >= 2", total)
	}
}
