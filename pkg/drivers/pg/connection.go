package pg

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// Connection wraps a pgxpool.Pool for one open profile. ServerVersion is
// cached at Open time (SELECT version() runs exactly once); subsequent
// ServerVersion() calls return the cached string without touching the pool.
//
// Close blocks until all sessions acquired via AcquireSession are themselves
// Closed (counter-managed via sessions). Calling Close with outstanding
// sessions is undefined behavior beyond the pool's own draining semantics —
// a single stderr WARN is emitted and pool.Close is invoked regardless. See
// epic dbsavvy-921 Arch-6 (review-plan resolutions).
type Connection struct {
	pool          *pgxpool.Pool
	serverVersion string
	sessions      atomic.Int32
	closeOnce     sync.Once
}

// Close releases the underlying pgxpool.Pool. Idempotent: second and
// subsequent calls return nil without re-closing the pool. If outstanding
// sessions are present (sessions > 0) a single stderr WARN is emitted before
// the pool is closed.
func (c *Connection) Close() error {
	c.closeOnce.Do(func() {
		if n := c.sessions.Load(); n > 0 {
			_, _ = fmt.Fprintf(os.Stderr, "WARN: pg: closing Connection with %d outstanding session(s); pool will drain them\n", n)
		}
		c.pool.Close()
	})
	return nil
}

// Ping forwards to the underlying pool. A non-nil ctx Deadline is honored.
func (c *Connection) Ping(ctx context.Context) error {
	return c.pool.Ping(ctx)
}

// ServerVersion returns the SELECT version() string captured at Open. It does
// not touch the pool — call cost is a single pointer load.
func (c *Connection) ServerVersion() string {
	return c.serverVersion
}

// AcquireSession returns drivers.ErrNotImplemented in v1; epic task
// dbsavvy-921.9 implements *pg.Session and replaces this stub with a real
// pgxpool.Acquire-backed checkout that increments c.sessions.
func (c *Connection) AcquireSession(_ context.Context) (drivers.Session, error) {
	return nil, drivers.ErrNotImplemented
}

// Cancel returns drivers.ErrNotImplemented in v1 — Capabilities.HasLiveCancel
// is correspondingly false. pg_cancel_backend wiring lands in epic E6 (see
// epic dbsavvy-921 D17).
func (c *Connection) Cancel(_ context.Context, _ models.QueryID) error {
	return drivers.ErrNotImplemented
}

var _ drivers.Connection = (*Connection)(nil)
