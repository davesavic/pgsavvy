package pg

import (
	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// NoticeRouter dispatches NOTICE / WARNING / INFO messages received via
// pgconn.Config.OnNotice (set exactly once at pool creation) to the per-Session
// channel registered with Subscribe. Routing is keyed on the *pgconn.PgConn
// pointer that pgx hands to the OnNotice callback: newSession bind()s the
// pgconn → SessionID mapping when it constructs a *Session, and Session.Close
// unbind()s it. AttachNotice on Session wires Subscribe; Session.Close also
// Unsubscribes — so a subscriber leak after Close is impossible.
//
// Sends are non-blocking: if the subscriber's channel buffer is full the
// notice is dropped and a per-session counter (DroppedNotices) increments. The
// buffer size is the caller's choice (cap=32 is the documented default for
// pkg/session.SQLSession).
//
// All operations are safe for concurrent use. The pgconn→SessionID map is a
// sync.Map; the SessionID→channel map is RWMutex-guarded so route can take a
// read lock for each dispatch.
type NoticeRouter struct {
	mu          sync.RWMutex
	subscribers map[models.SessionID]chan<- pgconn.Notice

	// connSession maps *pgconn.PgConn → models.SessionID. sync.Map is used
	// because the bind/unbind/route triad reads frequently from many
	// goroutines while writes happen only at session-open and session-close.
	connSession sync.Map

	// dropped maps models.SessionID → *atomic.Uint64. Allocated lazily on
	// first drop so the common "everything fits" path adds no allocation.
	dropped sync.Map
}

// NewNoticeRouter returns an empty router ready to wire as
// pgconn.Config.OnNotice. The returned value is the only legal source of the
// pool-level OnNotice closure produced by (*NoticeRouter).route.
func NewNoticeRouter() *NoticeRouter {
	return &NoticeRouter{
		subscribers: make(map[models.SessionID]chan<- pgconn.Notice),
	}
}

// Subscribe registers ch as the notice destination for sid. A second
// Subscribe for the same sid overwrites the prior channel — the typical
// caller is (*Session).AttachNotice, which is invoked at most once per
// Session, but a re-Attach is well-defined.
func (r *NoticeRouter) Subscribe(sid models.SessionID, ch chan<- pgconn.Notice) {
	r.mu.Lock()
	r.subscribers[sid] = ch
	r.mu.Unlock()
}

// Unsubscribe removes any channel previously registered for sid. Safe to call
// for an sid that was never Subscribed. Called by Session.Close.
func (r *NoticeRouter) Unsubscribe(sid models.SessionID) {
	r.mu.Lock()
	delete(r.subscribers, sid)
	r.mu.Unlock()
	// The dropped counter intentionally lingers: the value may still be read
	// by DroppedNotices after Close in diagnostic flows. It is GC'd with the
	// Router itself.
}

// bindConn associates pgc with sid so a subsequent route() invocation can find
// the subscriber. Called by newSession. pgc==nil is a no-op (defensive — pgx
// always passes a non-nil *PgConn from OnNotice and a non-nil pgxpool.Conn
// from Acquire).
func (r *NoticeRouter) bindConn(pgc *pgconn.PgConn, sid models.SessionID) {
	if pgc == nil {
		return
	}
	r.connSession.Store(pgc, sid)
}

// unbindConn removes the pgc → sid mapping. Called by Session.Close.
func (r *NoticeRouter) unbindConn(pgc *pgconn.PgConn) {
	if pgc == nil {
		return
	}
	r.connSession.Delete(pgc)
}

// route is the pgconn.NoticeHandler implementation. pgx invokes it from the
// connection's read loop on every NoticeResponse / WarningResponse frame —
// severity is preserved in n.Severity ("NOTICE" / "WARNING" / "INFO" / etc.).
// Lookup misses (unknown pgconn, unsubscribed session) are silent so that
// notices delivered between Session.Close and a final pool recycle do not
// panic. Sends are non-blocking; a full channel increments the drop counter.
func (r *NoticeRouter) route(pgc *pgconn.PgConn, n *pgconn.Notice) {
	if pgc == nil || n == nil {
		return
	}
	sidV, ok := r.connSession.Load(pgc)
	if !ok {
		return
	}
	sid := sidV.(models.SessionID)
	r.mu.RLock()
	ch, ok := r.subscribers[sid]
	r.mu.RUnlock()
	if !ok {
		return
	}
	select {
	case ch <- *n:
	default:
		r.incrementDropped(sid)
	}
}

// incrementDropped bumps the per-session drop counter, lazily creating it.
func (r *NoticeRouter) incrementDropped(sid models.SessionID) {
	if v, ok := r.dropped.Load(sid); ok {
		v.(*atomic.Uint64).Add(1)
		return
	}
	c := new(atomic.Uint64)
	actual, loaded := r.dropped.LoadOrStore(sid, c)
	if loaded {
		c = actual.(*atomic.Uint64)
	}
	c.Add(1)
}

// droppedFor returns the count of notices dropped for sid because the
// subscriber's channel was full at delivery time. Zero when sid has no
// recorded drops (including when it was never Subscribed).
func (r *NoticeRouter) droppedFor(sid models.SessionID) uint64 {
	v, ok := r.dropped.Load(sid)
	if !ok {
		return 0
	}
	return v.(*atomic.Uint64).Load()
}
