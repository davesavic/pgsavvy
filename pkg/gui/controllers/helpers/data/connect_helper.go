package data

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// ErrSessionReplaced is returned by a queued LoadX call when the ConnectHelper
// it was submitted to has had its Session swapped out (Disconnect, or a
// Disconnect-then-Connect reconnect cycle) before the worker reached the
// closure. Callers SHOULD re-issue the load against the new Session.
var ErrSessionReplaced = errors.New("data: session replaced before queued call ran")

// errNotConnected is returned by LoadX calls made before Connect or after
// Disconnect. It is unexported — callers see "data: not connected" via
// err.Error() and SHOULD treat it as a programming error (the helper's
// lifecycle is owned by the controller, which must Connect before issuing
// loads).
var errNotConnected = errors.New("data: not connected")

// ConnectHelper owns the drivers.Connection / drivers.Session pair for a
// single connection profile and serializes every Session method call through
// a per-Session worker goroutine. Sessions are NOT safe for concurrent use
// (drivers.driver.go §Session and dbsavvy-921 D18), so the helper acts as the
// gatekeeper: controllers and other helpers call ConnectHelper.LoadX
// concurrently; the worker queue runs at most one closure at a time against
// the underlying Session.
//
// Lifecycle:
//
//  1. NewConnectHelper returns an empty helper. Construction is cheap and
//     does NOT touch the driver registry.
//  2. Connect(ctx, profile) looks up the driver factory via drivers.Get,
//     constructs the driver, calls Open + AcquireSession, and starts a fresh
//     worker goroutine bound to the new Session. Returns the underlying
//     Connection and Session for callers that want direct access; the
//     helper's serialized LoadX wrappers reach the same Session via the queue.
//  3. LoadSchemas / LoadTables / LoadColumns / LoadIndexes submit a closure
//     to the queue and block on its result. ctx cancellation cancels both the
//     wait AND the closure's own ctx; an already-running closure observes ctx
//     and returns from its driver call.
//  4. Disconnect closes the worker queue, waits for any in-flight closure to
//     finish, then closes the Session and Connection. After Disconnect the
//     helper is reusable: Connect may be called again with a new profile and
//     starts a fresh worker.
//
// Concurrent calls to Connect / Disconnect on the SAME helper from different
// goroutines are not supported (lifecycle is single-threaded by contract).
// Concurrent LoadX calls during the steady state are the supported pattern.
type ConnectHelper struct {
	mu      sync.Mutex
	state   *helperState // nil ⇔ not connected
	stateMu sync.RWMutex // protects publish/unpublish of state pointer
}

// helperState bundles every field that lives for the duration of a single
// Connection. Disconnect zeroes the parent's state pointer; the worker
// goroutine still has its own reference and drains the queue cleanly.
type helperState struct {
	conn    drivers.Connection
	session drivers.Session
	queue   chan workItem
	done    chan struct{} // closed by the worker after the queue drains
}

// workItem is one queued closure plus its caller's reply channel and ctx.
type workItem struct {
	ctx  context.Context
	run  func(context.Context) error
	done chan error
}

// NewConnectHelper returns a fresh helper. Construction does not touch
// drivers.Get and is safe to perform during gui wiring before any driver is
// known.
func NewConnectHelper() *ConnectHelper {
	return &ConnectHelper{}
}

// Connect resolves the driver, opens a Connection, acquires a Session, and
// starts the per-Session worker goroutine. The returned (Connection, Session)
// pair is the same instance the helper holds; callers MUST NOT call methods
// on Session directly while LoadX wrappers are in use — every call must go
// through the queue.
//
// Errors from drivers.Get (e.g. ErrUnknownDriver) propagate verbatim; errors
// from Open / AcquireSession propagate wrapped.
func (h *ConnectHelper) Connect(ctx context.Context, profile *models.Connection) (drivers.Connection, drivers.Session, error) {
	if profile == nil {
		return nil, nil, errors.New("data: nil profile")
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	h.stateMu.RLock()
	already := h.state != nil
	h.stateMu.RUnlock()
	if already {
		return nil, nil, errors.New("data: already connected (call Disconnect first)")
	}

	factory, err := drivers.Get(profile.Driver)
	if err != nil {
		return nil, nil, err
	}
	drv, err := factory(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("data: driver factory: %w", err)
	}
	conn, err := drv.Open(ctx, *profile)
	if err != nil {
		return nil, nil, err
	}
	sess, err := conn.AcquireSession(ctx)
	if err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("data: acquire session: %w", err)
	}

	st := &helperState{
		conn:    conn,
		session: sess,
		// Small buffer: most callers fire one-at-a-time; the buffer lets
		// burst-of-two (e.g. concurrent left-rail refreshes from two
		// controllers) enqueue without blocking the producer.
		queue: make(chan workItem, 8),
		done:  make(chan struct{}),
	}

	go runWorker(st)

	h.stateMu.Lock()
	h.state = st
	h.stateMu.Unlock()

	return conn, sess, nil
}

// Disconnect closes the worker queue, waits for the in-flight closure (if
// any) to finish, then closes the Session and Connection. Disconnect is safe
// to call when the helper is not connected (no-op). After Disconnect the
// helper may be Connect'd again with a new profile.
func (h *ConnectHelper) Disconnect() {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.stateMu.Lock()
	st := h.state
	h.state = nil
	h.stateMu.Unlock()

	if st == nil {
		return
	}

	// Close the queue: the worker drains queued items with ErrSessionReplaced
	// and exits after observing the channel close.
	close(st.queue)
	// Wait for the worker to publish done — guarantees Session.Close races
	// no in-flight call.
	<-st.done

	_ = st.session.Close()
	_ = st.conn.Close()
}

// Connection returns the live drivers.Connection or nil when not connected.
// The returned reference is invalidated by Disconnect.
func (h *ConnectHelper) Connection() drivers.Connection {
	h.stateMu.RLock()
	defer h.stateMu.RUnlock()
	if h.state == nil {
		return nil
	}
	return h.state.conn
}

// Session returns the live drivers.Session or nil when not connected. The
// returned reference is invalidated by Disconnect. Callers SHOULD prefer the
// LoadX wrappers to direct Session use, since the wrappers honor the
// per-Session serialization contract.
func (h *ConnectHelper) Session() drivers.Session {
	h.stateMu.RLock()
	defer h.stateMu.RUnlock()
	if h.state == nil {
		return nil
	}
	return h.state.session
}

// LoadSchemas wraps drivers.Session.ListSchemas. The signature matches the
// underlying interface verbatim (M07a: drivers.Session.ListSchemas takes a
// `db` parameter that is documented-ignored by the Postgres driver in v1; the
// helper preserves it so a future multi-database engine can route on it).
func (h *ConnectHelper) LoadSchemas(ctx context.Context, db string) ([]models.Schema, error) {
	var out []models.Schema
	err := h.submit(ctx, func(ctx context.Context) error {
		sess, err := h.requireSession()
		if err != nil {
			return err
		}
		out, err = sess.ListSchemas(ctx, db)
		return err
	})
	return out, err
}

// LoadTables wraps drivers.Session.ListTables. Returns a pointer slice per
// drivers.Session.ListTables — *models.Table embeds atomic counters and
// cannot be safely value-copied.
func (h *ConnectHelper) LoadTables(ctx context.Context, schema string) ([]*models.Table, error) {
	var out []*models.Table
	err := h.submit(ctx, func(ctx context.Context) error {
		sess, err := h.requireSession()
		if err != nil {
			return err
		}
		out, err = sess.ListTables(ctx, schema)
		return err
	})
	return out, err
}

// LoadColumns wraps drivers.Session.ListColumns.
func (h *ConnectHelper) LoadColumns(ctx context.Context, schema, table string) ([]models.Column, error) {
	var out []models.Column
	err := h.submit(ctx, func(ctx context.Context) error {
		sess, err := h.requireSession()
		if err != nil {
			return err
		}
		out, err = sess.ListColumns(ctx, schema, table)
		return err
	})
	return out, err
}

// LoadIndexes wraps drivers.Session.ListIndexes.
func (h *ConnectHelper) LoadIndexes(ctx context.Context, schema, table string) ([]models.Index, error) {
	var out []models.Index
	err := h.submit(ctx, func(ctx context.Context) error {
		sess, err := h.requireSession()
		if err != nil {
			return err
		}
		out, err = sess.ListIndexes(ctx, schema, table)
		return err
	})
	return out, err
}

// requireSession returns the current Session or errNotConnected. It must be
// called from inside the worker goroutine: when the helper is reconnected
// (Disconnect+Connect) BEFORE a queued item runs, the queued item observes
// the old state's queue close before reaching here, so requireSession in the
// closure path always sees a live Session of the SAME state that owned the
// closure.
func (h *ConnectHelper) requireSession() (drivers.Session, error) {
	h.stateMu.RLock()
	defer h.stateMu.RUnlock()
	if h.state == nil {
		return nil, errNotConnected
	}
	return h.state.session, nil
}

// submit enqueues run on the current Session's worker and blocks until either
// the worker invokes the closure and the closure returns, or ctx is canceled,
// or the queue is closed (Disconnect / reconnect) before the closure ran.
func (h *ConnectHelper) submit(ctx context.Context, run func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// Fast-path ctx check: if already canceled, don't spend a queue slot.
	if err := ctx.Err(); err != nil {
		return err
	}

	h.stateMu.RLock()
	st := h.state
	h.stateMu.RUnlock()
	if st == nil {
		return errNotConnected
	}

	item := workItem{
		ctx:  ctx,
		run:  run,
		done: make(chan error, 1),
	}

	// Enqueue. If the queue is full we block on send; ctx still wins via
	// select. If the queue was closed under our feet (Disconnect after the
	// state-pointer check), the send panics — we recover via a deferred
	// recover and return ErrSessionReplaced.
	enqErr := safeSend(st.queue, item, ctx)
	if enqErr != nil {
		return enqErr
	}

	select {
	case err := <-item.done:
		return err
	case <-ctx.Done():
		// Note: ctx cancellation here cannot un-enqueue the item. The worker
		// will still invoke run with the canceled ctx; run.ctx.Err() will be
		// non-nil and the driver call will short-circuit. We still return
		// ctx.Err() to the caller immediately so the controller is not
		// blocked.
		return ctx.Err()
	}
}

// safeSend sends item to queue and reports nil on success, ctx.Err() on ctx
// cancellation while the queue is full, or ErrSessionReplaced if the queue
// was closed before the send completed.
func safeSend(queue chan<- workItem, item workItem, ctx context.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			// Recover from "send on closed channel" — Disconnect closed the
			// queue between our state.RLock and the send.
			err = ErrSessionReplaced
		}
	}()
	select {
	case queue <- item:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// runWorker is the per-Session goroutine. It pulls workItems off queue in
// FIFO order, invokes run with each item's ctx, and writes the result to
// item.done. When the queue closes the worker signals st.done and exits;
// any items observed BEFORE the close are run; the channel itself drops
// items added after close (Go's standard close semantics).
func runWorker(st *helperState) {
	defer close(st.done)
	for item := range st.queue {
		// Fast-path: caller may have canceled before we picked up the item.
		if err := item.ctx.Err(); err != nil {
			item.done <- err
			continue
		}
		item.done <- item.run(item.ctx)
	}
}
