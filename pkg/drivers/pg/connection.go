package pg

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sirupsen/logrus"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/logs"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// cancelRequestCode is the magic 4-byte protocol code that identifies a
// CancelRequest packet on the PostgreSQL wire (1234 << 16 | 5678). See
// https://www.postgresql.org/docs/current/protocol-message-formats.html
// ("CancelRequest").
const cancelRequestCode uint32 = 80877102

// Connection wraps a pgxpool.Pool for one open profile. ServerVersion is
// cached at Open time (SELECT version() runs exactly once); subsequent
// ServerVersion() calls return the cached string without touching the pool.
//
// Close blocks until all sessions acquired via AcquireSession are themselves
// Closed (counter-managed via sessions). Calling Close with outstanding
// sessions is undefined behavior beyond the pool's own draining semantics —
// a single stderr WARN is emitted and pool.Close is invoked regardless. See
// epic dbsavvy-921 Arch-6 (review-plan resolutions).
//
// cancelKeys maps each live session's BackendPID to its secret key so that
// Connection.Cancel — given only a QueryID — can synthesize the wire-level
// CancelRequest packet without touching the (busy) session's pgconn. Entries
// are written by newSession and removed by Session.Close. See epic
// dbsavvy-66p.4.
type Connection struct {
	pool              *pgxpool.Pool
	serverVersion     string
	majorVersion      int
	sessions          atomic.Int32
	closeOnce         sync.Once
	pgVersionWarnOnce sync.Once

	cancelMu   sync.RWMutex
	cancelKeys map[uint32]uint32 // BackendPID -> SecretKey

	// notices is the pool-level NOTICE/WARNING/INFO router (epic dbsavvy-66p.5).
	// cfg.ConnConfig.OnNotice is set to notices.route exactly once at pool
	// creation in Driver.Open. Per-session subscription and pgconn↔SessionID
	// bookkeeping is performed by newSession / Session.Close.
	notices *NoticeRouter
}

// Close releases the underlying pgxpool.Pool. Idempotent: second and
// subsequent calls return nil without re-closing the pool. If outstanding
// sessions are present (sessions > 0) a single stderr WARN is emitted before
// the pool is closed.
func (c *Connection) Close() error {
	c.closeOnce.Do(func() {
		outstanding := c.sessions.Load()
		if outstanding > 0 {
			_, _ = fmt.Fprintf(os.Stderr, "WARN: pg: closing Connection with %d outstanding session(s); pool will drain them\n", outstanding)
		}
		logs.Event(pkgLogger(), "db", "conn_close", logrus.Fields{
			"outstanding_sessions": outstanding,
		})
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

// AcquireSession checks out a pgxpool connection and wraps it in a *Session.
// The Session takes ownership of the pooled connection; the caller MUST call
// Session.Close() to return it to the pool. AcquireSession increments the
// outstanding-sessions counter; Session.Close decrements it.
func (c *Connection) AcquireSession(ctx context.Context) (drivers.Session, error) {
	pgxConn, err := c.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("pg: acquire session: %w", err)
	}
	c.sessions.Add(1)
	return newSession(pgxConn, c), nil
}

// warnIfPostgresGE18 emits at most one stderr WARN per Connection when the
// server reports PostgreSQL 18 or newer. Called by every Session list-method
// so the operator learns about catalog drift the first time they browse
// schema metadata, but is not spammed on every keystroke. See epic
// dbsavvy-921.9 scope expansion #2.
func (c *Connection) warnIfPostgresGE18() {
	if c.majorVersion < 18 {
		return
	}
	c.pgVersionWarnOnce.Do(func() {
		_, _ = fmt.Fprintf(os.Stderr, "WARN: pg: server reports PostgreSQL %d which is newer than tested; introspection queries target Postgres 17 catalogs\n", c.majorVersion)
	})
}

// registerCancel records the secret key for backendPID so that Cancel can
// authenticate a cancel-request packet for this backend. Called by newSession.
// Safe for concurrent use.
func (c *Connection) registerCancel(backendPID, secretKey uint32) {
	if backendPID == 0 {
		return
	}
	c.cancelMu.Lock()
	if c.cancelKeys == nil {
		c.cancelKeys = make(map[uint32]uint32)
	}
	c.cancelKeys[backendPID] = secretKey
	c.cancelMu.Unlock()
}

// unregisterCancel removes the registry entry for backendPID. Called by
// Session.Close. Safe for concurrent use; a no-op when the key is absent.
func (c *Connection) unregisterCancel(backendPID uint32) {
	if backendPID == 0 {
		return
	}
	c.cancelMu.Lock()
	delete(c.cancelKeys, backendPID)
	c.cancelMu.Unlock()
}

// lookupCancelKey returns (secretKey, true) when backendPID is registered.
// Returns (0, false) otherwise — Cancel uses this to refuse stale or unknown
// PIDs in tests/dev, but in production a missing key is permitted (the cancel
// is best-effort and pg will silently ignore unknown keys).
func (c *Connection) lookupCancelKey(backendPID uint32) (uint32, bool) {
	c.cancelMu.RLock()
	defer c.cancelMu.RUnlock()
	k, ok := c.cancelKeys[backendPID]
	return k, ok
}

// Cancel asks the PostgreSQL server to terminate the in-flight query identified
// by qid. It dials a FRESH TCP connection (using the pool's pgconn.Config
// DialFunc / Host / Port) and writes the 16-byte CancelRequest packet directly
// — the original session's pgconn is never touched, so Cancel works while that
// session is mid-Stream on a long-running query.
//
// Contract:
//   - qid.BackendPID == 0  → drivers.ErrInvalidQueryID (precondition violation)
//   - ctx already cancelled at entry → ctx.Err()
//   - qid.BackendPID unknown to this Connection → still attempts the cancel
//     dial with secretKey=0 (Postgres ignores cancels with the wrong key, so
//     the caller observes nil, matching the "Cancel for unknown PID returns
//     nil" AC). Note this matches Postgres protocol semantics — there is no
//     ACK for a cancel-request.
//   - Network / dial failure → wrapped pgconn/network error (NOT ErrInvalidQueryID).
//   - Success → nil (per Postgres protocol, success does NOT guarantee the
//     server actually terminated the query; the caller observes that via the
//     session's RowStream surfacing a 57014 error).
//
// Cancel is idempotent at the wire: two simultaneous calls each open their
// own cancel dial; the server accepts duplicates without error. See epic
// dbsavvy-66p.4 + DESIGN.md §12.4.
func (c *Connection) Cancel(ctx context.Context, qid models.QueryID) error {
	if qid.BackendPID == 0 {
		return drivers.ErrInvalidQueryID
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	log := pkgLogger()
	emitCancel := func(err error) {
		fields := logrus.Fields{
			"sid":         uint64(qid.SessionID),
			"qid_nonce":   qid.Nonce,
			"backend_pid": uint64(qid.BackendPID),
		}
		if err != nil {
			fields["err"] = err.Error()
		}
		logs.Event(log, "db", "query_cancel", fields)
	}

	cancelErr := c.cancelInner(ctx, qid)
	emitCancel(cancelErr)
	return cancelErr
}

// cancelInner is the implementation of Cancel without the instrumentation
// emit. Split out so Cancel emits exactly one query_cancel event regardless
// of which error branch fires.
func (c *Connection) cancelInner(ctx context.Context, qid models.QueryID) error {
	secretKey, _ := c.lookupCancelKey(qid.BackendPID)

	// Pool.Config() returns a defensive copy, so reading ConnConfig.Config is
	// race-free with respect to pool internals.
	cfg := c.pool.Config()
	if cfg == nil || cfg.ConnConfig == nil {
		return fmt.Errorf("pg: cancel: nil pool config")
	}
	pgconnCfg := cfg.ConnConfig.Config

	network, address := pgconn.NetworkAddress(pgconnCfg.Host, pgconnCfg.Port)

	dial := pgconnCfg.DialFunc
	if dial == nil {
		return fmt.Errorf("pg: cancel: nil DialFunc")
	}

	conn, err := dial(ctx, network, address)
	if err != nil {
		return fmt.Errorf("pg: cancel: dial: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// 16-byte CancelRequest: length(4) | code(4) | pid(4) | key(4)
	buf := make([]byte, 16)
	binary.BigEndian.PutUint32(buf[0:4], 16)
	binary.BigEndian.PutUint32(buf[4:8], cancelRequestCode)
	binary.BigEndian.PutUint32(buf[8:12], qid.BackendPID)
	binary.BigEndian.PutUint32(buf[12:16], secretKey)

	if _, err := conn.Write(buf); err != nil {
		return fmt.Errorf("pg: cancel: write: %w", err)
	}

	// Mirror libpq's behavior: wait for the server to close the conn before
	// returning. The read result is intentionally discarded — Postgres never
	// sends a reply on the cancel channel; the server simply closes after
	// processing. Ignoring any read error here keeps Cancel non-flaky on
	// platforms where the discard happens during conn.Close.
	_, _ = conn.Read(buf)
	return nil
}

var _ drivers.Connection = (*Connection)(nil)
