package session

import (
	"context"
	"errors"
	"sync"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// FKLoader resolves the foreign keys defined on (schema, table) on a cache
// miss. Implementations typically wrap drivers.Session.ListForeignKeys but
// the indirection keeps FKCache testable without a live driver.
type FKLoader func(ctx context.Context, schema, table string) ([]models.ForeignKey, error)

// fkKey identifies a cached entry. Schema + Table are the only inputs because
// FKs are scoped to a single owning table; the cache itself is per-Connection
// (one FKCache per SQLSession), so cross-connection collisions are impossible.
type fkKey struct {
	Schema string
	Table  string
}

// FKCache is an in-memory, per-Connection cache of foreign-key metadata.
// Cached entries are loaded lazily on first Get via the injected Loader and
// retained until Invalidate / InvalidateAll. Errors from the loader are NOT
// cached so a transient failure does not poison subsequent reads.
//
// Concurrency: a sync.RWMutex protects the map. Get takes the read lock for
// the hot path; only the slow path (cache miss) escalates to the write lock
// to insert. FKCache is safe for concurrent use under -race per task AC.
//
// Lifecycle: bound to a single SQLSession; closing the SQLSession drops the
// only reference so the cache is GC'd. No AppState persistence (ADR-8).
//
// Forward + reverse: Get resolves outbound FKs (the table's own FK
// constraints); GetReverse resolves inbound FKs (other tables' FKs
// that reference this table). Each direction has its own loader and
// its own cache map — they are intentionally independent so a forward
// Invalidate does not nuke reverse entries the user just paid for.
type FKCache struct {
	loader        FKLoader
	reverseLoader FKLoader

	mu             sync.RWMutex
	entries        map[fkKey][]models.ForeignKey
	reverseEntries map[fkKey][]models.ForeignKey
}

// NewFKCache returns an empty cache backed by loader. loader must be non-nil;
// passing nil panics at first Get rather than silently dropping requests.
// SetReverseLoader wires the inbound-FK loader; without it GetReverse
// returns an error.
func NewFKCache(loader FKLoader) *FKCache {
	return &FKCache{
		loader:         loader,
		entries:        make(map[fkKey][]models.ForeignKey),
		reverseEntries: make(map[fkKey][]models.ForeignKey),
	}
}

// SetReverseLoader installs the inbound-FK loader used by GetReverse.
// Safe to call before any GetReverse caller observes the cache.
func (c *FKCache) SetReverseLoader(rl FKLoader) {
	c.mu.Lock()
	c.reverseLoader = rl
	c.mu.Unlock()
}

// Get returns the cached FKs for (schema, table), loading them via the
// injected Loader on a cache miss. The empty-FK case is cached as a non-nil
// empty slice so subsequent calls do not re-hit the driver. Loader errors
// are NOT cached — the caller may retry.
//
// Two Gets racing for the same key may both invoke Loader; the last writer
// wins. This is intentionally simple: FK metadata is small + idempotent, so
// a redundant fetch is cheaper than serializing all callers behind a per-key
// mutex.
func (c *FKCache) Get(ctx context.Context, schema, table string) ([]models.ForeignKey, error) {
	key := fkKey{Schema: schema, Table: table}

	c.mu.RLock()
	if fks, ok := c.entries[key]; ok {
		c.mu.RUnlock()
		return fks, nil
	}
	c.mu.RUnlock()

	fks, err := c.loader(ctx, schema, table)
	if err != nil {
		return nil, err
	}
	if fks == nil {
		fks = []models.ForeignKey{}
	}

	c.mu.Lock()
	c.entries[key] = fks
	c.mu.Unlock()

	return fks, nil
}

// GetReverse returns the cached inbound FKs for (schema, table) — every
// FK constraint whose referenced (target) table is (schema, table).
// Mirrors Get's hot/slow path and never-cache-errors semantics. Returns
// an error when no reverse loader has been wired via SetReverseLoader.
func (c *FKCache) GetReverse(ctx context.Context, schema, table string) ([]models.ForeignKey, error) {
	key := fkKey{Schema: schema, Table: table}

	c.mu.RLock()
	if fks, ok := c.reverseEntries[key]; ok {
		c.mu.RUnlock()
		return fks, nil
	}
	loader := c.reverseLoader
	c.mu.RUnlock()

	if loader == nil {
		return nil, errors.New("fk cache: reverse loader not wired")
	}

	fks, err := loader(ctx, schema, table)
	if err != nil {
		return nil, err
	}
	if fks == nil {
		fks = []models.ForeignKey{}
	}

	c.mu.Lock()
	c.reverseEntries[key] = fks
	c.mu.Unlock()

	return fks, nil
}

// Invalidate drops the cached entry for (schema, table) if present.
// Invalidating an absent key is a no-op. Only touches forward entries;
// reverse entries are dropped via InvalidateAll.
func (c *FKCache) Invalidate(schema, table string) {
	c.mu.Lock()
	delete(c.entries, fkKey{Schema: schema, Table: table})
	c.mu.Unlock()
}

// InvalidateAll clears every cached entry, forward and reverse. Called
// from the schema-rail refresh path so a manual rail reload drops every
// kind of stale FK metadata in one shot.
func (c *FKCache) InvalidateAll() {
	c.mu.Lock()
	// Replace the maps rather than ranging+delete so any in-flight reader
	// that has already returned its slice keeps a valid reference (the slice
	// is immutable post-store; we never mutate cached values in place).
	c.entries = make(map[fkKey][]models.ForeignKey)
	c.reverseEntries = make(map[fkKey][]models.ForeignKey)
	c.mu.Unlock()
}
