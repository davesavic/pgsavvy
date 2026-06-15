package data

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/logs"
	"github.com/davesavic/pgsavvy/pkg/models"
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
// (drivers.driver.go §Session), so the helper acts as the
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

	// funcDetailMu guards the function-detail cache and the warm-in-flight set.
	// Separate from stateMu so a cache read never contends with connect/disconnect
	// state churn.
	funcDetailMu       sync.Mutex
	funcDetail         map[string][]models.FunctionDetail // schema+name -> details; nil until first populate
	funcDetailInflight map[string]struct{}                // keys with a WarmFunctionDetail load in flight

	// uiThread schedules onReady callbacks onto the gocui MainLoop. Optional:
	// when nil (default / unit tests) callbacks run inline on the loading
	// goroutine. Set via SetUIScheduler during gui wiring so WarmFunctionDetail
	// honors the "onReady runs on the UI loop" contract.
	uiThread func(func() error)
	// log is the optional structured logger used for load-failure events. nil is
	// tolerated (logs.Event is nil-safe).
	log *slog.Logger
}

// SetUIScheduler injects the gocui MainLoop scheduler used by
// WarmFunctionDetail to run its onReady callback on the UI loop. Wiring code
// passes orchestrator.Gui.OnUIThread. Must be called before WarmFunctionDetail;
// it is not safe to call concurrently with a warm in flight.
func (h *ConnectHelper) SetUIScheduler(onUIThread func(func() error)) {
	h.uiThread = onUIThread
}

// SetLogger injects the structured logger for load-failure events. nil is
// tolerated. Not safe to call concurrently with loads in flight.
func (h *ConnectHelper) SetLogger(l *slog.Logger) {
	h.log = l
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
func (h *ConnectHelper) Connect(ctx context.Context, profile *models.Connection, reporter drivers.ProgressReporter) (drivers.Connection, drivers.Session, error) {
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
	conn, err := drv.Open(ctx, *profile, reporter)
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
		result, err := sess.ListSchemas(ctx, db)
		if err != nil {
			return err
		}
		out = result
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
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
		result, err := sess.ListTables(ctx, schema)
		if err != nil {
			return err
		}
		out = result
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// LoadColumns wraps drivers.Session.ListColumns.
func (h *ConnectHelper) LoadColumns(ctx context.Context, schema, table string) ([]models.Column, error) {
	var out []models.Column
	err := h.submit(ctx, func(ctx context.Context) error {
		sess, err := h.requireSession()
		if err != nil {
			return err
		}
		result, err := sess.ListColumns(ctx, schema, table)
		if err != nil {
			return err
		}
		out = result
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// LoadIndexes wraps drivers.Session.ListIndexes.
func (h *ConnectHelper) LoadIndexes(ctx context.Context, schema, table string) ([]models.Index, error) {
	var out []models.Index
	err := h.submit(ctx, func(ctx context.Context) error {
		sess, err := h.requireSession()
		if err != nil {
			return err
		}
		result, err := sess.ListIndexes(ctx, schema, table)
		if err != nil {
			return err
		}
		out = result
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// LoadForeignKeys wraps drivers.Session.ListForeignKeys.
func (h *ConnectHelper) LoadForeignKeys(ctx context.Context, schema, table string) ([]models.ForeignKey, error) {
	var out []models.ForeignKey
	err := h.submit(ctx, func(ctx context.Context) error {
		sess, err := h.requireSession()
		if err != nil {
			return err
		}
		result, err := sess.ListForeignKeys(ctx, schema, table)
		if err != nil {
			return err
		}
		out = result
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// LoadFunctions wraps drivers.Session.ListFunctions (FUNCTION-routine names
// only). Mirrors LoadColumns so the eager function-name warm routes through the
// SAME serialized worker queue rather than calling sess.ListFunctions on a raw
// Session pointer (which would race other queries on the pgx conn). Added for
// the schema-warmer eager tier.
func (h *ConnectHelper) LoadFunctions(ctx context.Context) ([]string, error) {
	var out []string
	err := h.submit(ctx, func(ctx context.Context) error {
		sess, err := h.requireSession()
		if err != nil {
			return err
		}
		result, err := sess.ListFunctions(ctx)
		if err != nil {
			return err
		}
		out = result
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// LoadFunctionDetail wraps drivers.Session.DescribeFunction. Mirrors
// LoadColumns: the DescribeFunction call runs ONLY inside submit() on the
// per-Session worker queue (never on the calling goroutine), honoring the
// per-Session serialization contract. On success the result is cached under
// schema+name so the synchronous FunctionDetail read can serve completion
// without blocking. A load error is logged via logs.Event and leaves the cache
// entry unpopulated.
func (h *ConnectHelper) LoadFunctionDetail(ctx context.Context, schema, name string) ([]models.FunctionDetail, error) {
	var out []models.FunctionDetail
	err := h.submit(ctx, func(ctx context.Context) error {
		sess, err := h.requireSession()
		if err != nil {
			return err
		}
		result, err := sess.DescribeFunction(ctx, schema, name)
		if err != nil {
			return err
		}
		out = result
		return nil
	})
	if err != nil {
		logs.Event(h.log, "completion", "load_function_detail_err",
			slog.String("schema", schema),
			slog.String("name", name),
			slog.String("err", err.Error()),
		)
		return nil, err
	}
	h.storeFunctionDetail(schema, name, out)
	return out, nil
}

// FunctionDetail is the synchronous cache read for completion's sync path. It
// returns the cached detail slice and found=true on a hit, or (nil, false) on a
// miss. It never blocks, never touches the Session, and never returns an error —
// a miss simply means LoadFunctionDetail / WarmFunctionDetail has not populated
// the key yet. The returned value is an independent deep copy: both the outer
// []FunctionDetail and each entry's Args slice have their own backing arrays, so
// the caller may mutate or append to them without corrupting the cache or
// aliasing a concurrent reader.
func (h *ConnectHelper) FunctionDetail(schema, name string) ([]models.FunctionDetail, bool) {
	key := funcDetailKey(schema, name)
	h.funcDetailMu.Lock()
	defer h.funcDetailMu.Unlock()
	v, ok := h.funcDetail[key]
	if !ok {
		return nil, false
	}
	return cloneFunctionDetails(v), true
}

// WarmFunctionDetail ensures the function-detail cache holds an entry for
// schema+name, invoking onReady exactly once after the cache is populated. On a
// cache hit it schedules onReady immediately (on the UI loop). On a miss it
// loads asynchronously through the per-Session worker (LoadFunctionDetail) and
// fires onReady on the UI loop once the cache is populated. The load is
// idempotent per key: a second WarmFunctionDetail for a key whose load is
// already in flight does not issue a second DescribeFunction round-trip (its
// onReady still fires when that single load completes). A load failure does NOT
// invoke onReady (the cache stays empty); the error is logged inside
// LoadFunctionDetail. A nil onReady is safe (the load still warms the cache).
func (h *ConnectHelper) WarmFunctionDetail(schema, name string, onReady func()) {
	if _, ok := h.FunctionDetail(schema, name); ok {
		h.fireOnReady(onReady)
		return
	}

	key := funcDetailKey(schema, name)
	h.funcDetailMu.Lock()
	if h.funcDetailInflight == nil {
		h.funcDetailInflight = make(map[string]struct{})
	}
	if _, inflight := h.funcDetailInflight[key]; inflight {
		// A load for this key is already running; that load's onReady will fire.
		// We could chain additional callbacks here, but the contract only
		// requires at-most-one round-trip and that *an* onReady fires once. The
		// caller that started the in-flight load owns the callback.
		h.funcDetailMu.Unlock()
		return
	}
	h.funcDetailInflight[key] = struct{}{}
	h.funcDetailMu.Unlock()

	go func() {
		_, err := h.LoadFunctionDetail(context.Background(), schema, name)

		h.funcDetailMu.Lock()
		delete(h.funcDetailInflight, key)
		h.funcDetailMu.Unlock()

		// A failed load leaves the cache unpopulated and does NOT signal ready.
		if err != nil {
			return
		}
		h.fireOnReady(onReady)
	}()
}

// fireOnReady runs onReady on the UI loop when a scheduler is wired, else
// inline. nil onReady is a no-op.
func (h *ConnectHelper) fireOnReady(onReady func()) {
	if onReady == nil {
		return
	}
	if h.uiThread == nil {
		onReady()
		return
	}
	h.uiThread(func() error {
		onReady()
		return nil
	})
}

// storeFunctionDetail populates the cache for schema+name. A nil/empty detail
// slice still records the key as populated (a function with no rows described is
// a legitimate empty result, distinct from a miss). The stored value is a deep
// copy (outer slice + each entry's Args), so the cache shares no mutable backing
// array with the caller-supplied slice nor with any value later returned by
// FunctionDetail.
func (h *ConnectHelper) storeFunctionDetail(schema, name string, details []models.FunctionDetail) {
	key := funcDetailKey(schema, name)
	cp := cloneFunctionDetails(details)
	h.funcDetailMu.Lock()
	defer h.funcDetailMu.Unlock()
	if h.funcDetail == nil {
		h.funcDetail = make(map[string][]models.FunctionDetail)
	}
	h.funcDetail[key] = cp
}

// cloneFunctionDetails returns a deep copy of details: a fresh outer slice and,
// for each entry, a fresh Args backing array. FunctionArg fields are all strings,
// so an element copy fully isolates them. A nil input yields an empty (non-nil)
// slice so a stored empty result reads back as a non-nil hit.
func cloneFunctionDetails(details []models.FunctionDetail) []models.FunctionDetail {
	cp := make([]models.FunctionDetail, len(details))
	copy(cp, details)
	for i := range cp {
		if cp[i].Args == nil {
			continue
		}
		args := make([]models.FunctionArg, len(cp[i].Args))
		copy(args, cp[i].Args)
		cp[i].Args = args
	}
	return cp
}

// funcDetailKey builds the cache key from a schema + function name. Schema and
// name cannot contain a NUL byte, so it is an unambiguous separator.
func funcDetailKey(schema, name string) string {
	return schema + "\x00" + name
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
//
// Caller invariant: when submit returns a non-nil error, the worker may still
// be running the closure concurrently. Callers MUST treat any closure-captured
// state (variables written by `run`) as undefined in that branch — reading it
// races with the worker. Only the success path (nil error) provides a
// happens-before edge to closure writes via the done-channel receive.
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
