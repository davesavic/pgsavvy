package data

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// --- fakes ----------------------------------------------------------------

// fakeSession satisfies drivers.Session for unit tests. It records the number
// of concurrent in-flight calls so the serialization test can assert the
// queue never lets two closures run at once.
type fakeSession struct {
	id            models.SessionID
	closed        atomic.Bool
	inFlight      atomic.Int32
	maxInFlight   atomic.Int32
	tablesCalls   atomic.Int32
	tablesBlock   chan struct{} // when non-nil, ListTables blocks on receive
	tablesObserve chan struct{} // when non-nil, ListTables sends here on entry
	listTablesErr error

	describeFuncCalls atomic.Int32
	describeFuncErr   error
	describeFuncOut   []models.FunctionDetail
}

func (s *fakeSession) Close() error {
	s.closed.Store(true)
	return nil
}
func (s *fakeSession) ID() models.SessionID { return s.id }

func (s *fakeSession) ListDatabases(_ context.Context) ([]models.Database, error) {
	return nil, nil
}

func (s *fakeSession) ListSchemas(_ context.Context, _ string) ([]models.Schema, error) {
	return []models.Schema{{Name: "public"}}, nil
}

func (s *fakeSession) ListTables(ctx context.Context, _ string) ([]*models.Table, error) {
	s.tablesCalls.Add(1)
	n := s.inFlight.Add(1)
	defer s.inFlight.Add(-1)
	// Track the high-water mark of concurrent in-flight calls.
	for {
		cur := s.maxInFlight.Load()
		if n <= cur {
			break
		}
		if s.maxInFlight.CompareAndSwap(cur, n) {
			break
		}
	}
	if s.tablesObserve != nil {
		select {
		case s.tablesObserve <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.tablesBlock != nil {
		select {
		case <-s.tablesBlock:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.listTablesErr != nil {
		return nil, s.listTablesErr
	}
	return []*models.Table{{Schema: "public", Name: "t"}}, nil
}

func (s *fakeSession) ListColumns(_ context.Context, _, _ string) ([]models.Column, error) {
	return []models.Column{{Name: "c", Position: 1}}, nil
}

func (s *fakeSession) ListIndexes(_ context.Context, _, _ string) ([]models.Index, error) {
	return []models.Index{{Name: "ix"}}, nil
}

func (s *fakeSession) ListConstraints(_ context.Context, _, _ string) ([]models.Constraint, error) {
	return nil, nil
}

func (s *fakeSession) ListForeignKeys(_ context.Context, _, _ string) ([]models.ForeignKey, error) {
	return nil, nil
}

func (s *fakeSession) ListInboundForeignKeys(_ context.Context, _, _ string) ([]models.ForeignKey, error) {
	return nil, nil
}

func (s *fakeSession) ListFunctions(_ context.Context) ([]string, error) {
	return nil, nil
}

func (s *fakeSession) DescribeFunction(_ context.Context, _, _ string) ([]models.FunctionDetail, error) {
	s.describeFuncCalls.Add(1)
	if s.describeFuncErr != nil {
		return nil, s.describeFuncErr
	}
	return s.describeFuncOut, nil
}

func (s *fakeSession) Execute(_ context.Context, _ models.Query) (models.Result, error) {
	return models.Result{}, nil
}

func (s *fakeSession) Stream(_ context.Context, _ models.Query) (drivers.RowStream, error) {
	return nil, nil
}

func (s *fakeSession) Explain(_ context.Context, _ models.Query, _ bool) (models.Plan, error) {
	return models.Plan{}, nil
}

func (s *fakeSession) Begin(_ context.Context, _ models.TxOptions) (drivers.Transaction, error) {
	return nil, nil
}
func (s *fakeSession) InTransaction() bool                     { return false }
func (s *fakeSession) CurrentTransaction() drivers.Transaction { return nil }
func (s *fakeSession) Encoder() drivers.Encoder                { return nopEncoder{} }

// nopEncoder is a no-op drivers.Encoder used by the connect-helper fake
// session. It returns "NULL" for any input — these unit tests never
// exercise literal encoding.
type nopEncoder struct{}

func (nopEncoder) EncodeLiteral(_ any, _ uint32) string { return "NULL" }

// fakeConnection satisfies drivers.Connection.
type fakeConnection struct {
	sess     *fakeSession
	closed   atomic.Bool
	pingErr  error
	acqErr   error
	acqCount atomic.Int32
}

func (c *fakeConnection) Close() error {
	c.closed.Store(true)
	return nil
}
func (c *fakeConnection) Ping(_ context.Context) error { return c.pingErr }
func (c *fakeConnection) ServerVersion() string        { return "fake-1.0" }
func (c *fakeConnection) AcquireSession(_ context.Context) (drivers.Session, error) {
	c.acqCount.Add(1)
	if c.acqErr != nil {
		return nil, c.acqErr
	}
	return c.sess, nil
}

func (c *fakeConnection) Cancel(_ context.Context, _ models.QueryID) error {
	return nil
}

// fakeDriver satisfies drivers.Driver and lets the test pin Open's return.
type fakeDriver struct {
	conn    *fakeConnection
	openErr error
}

func (d *fakeDriver) Name() string                       { return "fake" }
func (d *fakeDriver) Capabilities() drivers.Capabilities { return drivers.Capabilities{} }
func (d *fakeDriver) Open(_ context.Context, _ drivers.ConnectionProfile, _ drivers.ProgressReporter) (drivers.Connection, error) {
	if d.openErr != nil {
		return nil, d.openErr
	}
	return d.conn, nil
}

// registerFake registers a fake driver under name and returns a cleanup
// closure. Because pkg/drivers exposes no public Unregister, we can only
// register once per process per name — but each test uses a UNIQUE name so
// there is no collision.
func registerFake(t *testing.T, name string, drv *fakeDriver) {
	t.Helper()
	drivers.Register(name, func(_ context.Context) (drivers.Driver, error) {
		return drv, nil
	})
}

// --- tests ----------------------------------------------------------------

func TestConnectHelperConnectPropagatesUnknownDriver(t *testing.T) {
	h := NewConnectHelper()
	_, _, err := h.Connect(context.Background(), &models.Connection{
		Name:   "p",
		Driver: "definitely-not-registered-xyz",
	}, nil)
	if err == nil {
		t.Fatal("expected error for unknown driver")
	}
	if !errors.Is(err, drivers.ErrUnknownDriver) {
		t.Fatalf("expected wrapped ErrUnknownDriver, got %v", err)
	}
}

func TestConnectHelperConnectNilProfile(t *testing.T) {
	h := NewConnectHelper()
	_, _, err := h.Connect(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected error for nil profile")
	}
}

func TestConnectHelperLoadXSerializesConcurrentCalls(t *testing.T) {
	sess := &fakeSession{
		tablesBlock:   make(chan struct{}),
		tablesObserve: make(chan struct{}, 2),
	}
	conn := &fakeConnection{sess: sess}
	drv := &fakeDriver{conn: conn}
	registerFake(t, "fake-serialize", drv)

	h := NewConnectHelper()
	_, _, err := h.Connect(context.Background(), &models.Connection{Name: "p", Driver: "fake-serialize"}, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(h.Disconnect)

	// Fire two LoadTables in parallel.
	results := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	for range 2 {
		go func() {
			defer wg.Done()
			_, err := h.LoadTables(context.Background(), "public")
			results <- err
		}()
	}

	// Observe first entry into ListTables.
	select {
	case <-sess.tablesObserve:
	case <-time.After(2 * time.Second):
		t.Fatal("first ListTables never entered the fake")
	}

	// Assert NO second entry until we release the first.
	select {
	case <-sess.tablesObserve:
		t.Fatalf("second ListTables entered fake while first was still running — serialization broken")
	case <-time.After(50 * time.Millisecond):
		// Expected: queue is holding the second item back.
	}

	// Release the first call; the second enters and we release it too.
	sess.tablesBlock <- struct{}{}
	select {
	case <-sess.tablesObserve:
	case <-time.After(2 * time.Second):
		t.Fatal("second ListTables never entered the fake after first released")
	}
	sess.tablesBlock <- struct{}{}

	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Errorf("LoadTables: %v", err)
		}
	}

	if got := sess.maxInFlight.Load(); got != 1 {
		t.Fatalf("max in-flight = %d, want 1 (serialization broken)", got)
	}
	if got := sess.tablesCalls.Load(); got != 2 {
		t.Fatalf("ListTables call count = %d, want 2", got)
	}
}

func TestConnectHelperDisconnectWaitsForInFlight(t *testing.T) {
	sess := &fakeSession{
		tablesBlock:   make(chan struct{}),
		tablesObserve: make(chan struct{}, 1),
	}
	conn := &fakeConnection{sess: sess}
	drv := &fakeDriver{conn: conn}
	registerFake(t, "fake-disconnect", drv)

	h := NewConnectHelper()
	_, _, err := h.Connect(context.Background(), &models.Connection{Name: "p", Driver: "fake-disconnect"}, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	loadDone := make(chan error, 1)
	go func() {
		_, err := h.LoadTables(context.Background(), "public")
		loadDone <- err
	}()

	// Wait until the in-flight call is actually inside the fake.
	select {
	case <-sess.tablesObserve:
	case <-time.After(2 * time.Second):
		t.Fatal("LoadTables never entered fake")
	}

	disconnectDone := make(chan struct{})
	go func() {
		h.Disconnect()
		close(disconnectDone)
	}()

	// Disconnect MUST NOT complete while the closure is still blocked inside
	// the fake: it owns the close-Session step.
	select {
	case <-disconnectDone:
		t.Fatal("Disconnect returned before in-flight closure completed")
	case <-time.After(50 * time.Millisecond):
	}

	// Release the fake; Disconnect can now proceed.
	sess.tablesBlock <- struct{}{}

	select {
	case err := <-loadDone:
		if err != nil {
			t.Fatalf("LoadTables: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("LoadTables never returned")
	}
	select {
	case <-disconnectDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Disconnect never completed")
	}
	if !sess.closed.Load() {
		t.Fatal("Session.Close not invoked by Disconnect")
	}
	if !conn.closed.Load() {
		t.Fatal("Connection.Close not invoked by Disconnect")
	}
}

func TestConnectHelperLoadBeforeConnect(t *testing.T) {
	h := NewConnectHelper()
	_, err := h.LoadSchemas(context.Background(), "")
	if err == nil {
		t.Fatal("expected error when LoadSchemas called before Connect")
	}
}

func TestConnectHelperReconnect(t *testing.T) {
	sess := &fakeSession{}
	conn := &fakeConnection{sess: sess}
	drv := &fakeDriver{conn: conn}
	registerFake(t, "fake-reconnect", drv)

	h := NewConnectHelper()
	if _, _, err := h.Connect(context.Background(), &models.Connection{Name: "p", Driver: "fake-reconnect"}, nil); err != nil {
		t.Fatalf("first Connect: %v", err)
	}
	if _, err := h.LoadSchemas(context.Background(), ""); err != nil {
		t.Fatalf("first LoadSchemas: %v", err)
	}
	h.Disconnect()

	if _, err := h.LoadSchemas(context.Background(), ""); err == nil {
		t.Fatal("LoadSchemas after Disconnect should error")
	}

	// Re-Connect with the same registered driver name.
	if _, _, err := h.Connect(context.Background(), &models.Connection{Name: "p", Driver: "fake-reconnect"}, nil); err != nil {
		t.Fatalf("second Connect: %v", err)
	}
	if _, err := h.LoadSchemas(context.Background(), ""); err != nil {
		t.Fatalf("post-reconnect LoadSchemas: %v", err)
	}
	h.Disconnect()
}

func TestConnectHelperLoadXContextCancellation(t *testing.T) {
	sess := &fakeSession{
		tablesBlock:   make(chan struct{}),
		tablesObserve: make(chan struct{}, 1),
	}
	conn := &fakeConnection{sess: sess}
	drv := &fakeDriver{conn: conn}
	registerFake(t, "fake-cancel", drv)

	h := NewConnectHelper()
	_, _, err := h.Connect(context.Background(), &models.Connection{Name: "p", Driver: "fake-cancel"}, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() {
		// Release the in-flight call so Disconnect can complete.
		select {
		case sess.tablesBlock <- struct{}{}:
		default:
		}
		h.Disconnect()
	})

	ctx, cancel := context.WithCancel(context.Background())
	loadDone := make(chan error, 1)
	go func() {
		_, err := h.LoadTables(ctx, "public")
		loadDone <- err
	}()
	<-sess.tablesObserve
	cancel()
	select {
	case err := <-loadDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("LoadTables did not return after ctx cancel")
	}
}

func TestConnectHelperDisconnectIdempotent(t *testing.T) {
	h := NewConnectHelper()
	// Disconnect when never-connected: must be no-op (and not panic).
	h.Disconnect()
	h.Disconnect()
}

func TestConnectHelperConnectionAndSessionAccessors(t *testing.T) {
	sess := &fakeSession{id: 7}
	conn := &fakeConnection{sess: sess}
	drv := &fakeDriver{conn: conn}
	registerFake(t, "fake-accessors", drv)

	h := NewConnectHelper()
	if h.Connection() != nil || h.Session() != nil {
		t.Fatal("accessors must return nil before Connect")
	}
	if _, _, err := h.Connect(context.Background(), &models.Connection{Name: "p", Driver: "fake-accessors"}, nil); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if h.Connection() != conn {
		t.Fatal("Connection accessor returned wrong instance")
	}
	if h.Session() != sess {
		t.Fatal("Session accessor returned wrong instance")
	}
	h.Disconnect()
	if h.Connection() != nil || h.Session() != nil {
		t.Fatal("accessors must return nil after Disconnect")
	}
}

// --- LoadFunctionDetail / FunctionDetail / WarmFunctionDetail -------------

func connectFakeFuncDetail(t *testing.T, name string, sess *fakeSession) *ConnectHelper {
	t.Helper()
	conn := &fakeConnection{sess: sess}
	drv := &fakeDriver{conn: conn}
	registerFake(t, name, drv)
	h := NewConnectHelper()
	if _, _, err := h.Connect(context.Background(), &models.Connection{Name: "p", Driver: name}, nil); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(h.Disconnect)
	return h
}

func TestConnectHelperLoadFunctionDetailPopulatesCache(t *testing.T) {
	want := []models.FunctionDetail{{Schema: "public", Name: "f", ReturnType: "int4"}}
	sess := &fakeSession{describeFuncOut: want}
	h := connectFakeFuncDetail(t, "fake-fd-load", sess)

	// Sync read before any load: miss, no block, no error.
	if got, ok := h.FunctionDetail("public", "f"); ok || got != nil {
		t.Fatalf("pre-load read = (%v,%v), want (nil,false)", got, ok)
	}

	got, err := h.LoadFunctionDetail(context.Background(), "public", "f")
	if err != nil {
		t.Fatalf("LoadFunctionDetail: %v", err)
	}
	if len(got) != 1 || got[0].Name != "f" {
		t.Fatalf("LoadFunctionDetail returned %v, want %v", got, want)
	}

	cached, ok := h.FunctionDetail("public", "f")
	if !ok {
		t.Fatal("FunctionDetail miss after successful load")
	}
	if len(cached) != 1 || cached[0].ReturnType != "int4" {
		t.Fatalf("cached = %v, want %v", cached, want)
	}

	// Defensive copy: mutating the returned slice must not corrupt the cache.
	cached[0].ReturnType = "MUTATED"
	again, _ := h.FunctionDetail("public", "f")
	if again[0].ReturnType != "int4" {
		t.Fatalf("cache mutated through returned slice: %q", again[0].ReturnType)
	}
}

func TestConnectHelperLoadFunctionDetailErrorLeavesCacheEmpty(t *testing.T) {
	sess := &fakeSession{describeFuncErr: errors.New("boom")}
	h := connectFakeFuncDetail(t, "fake-fd-err", sess)

	_, err := h.LoadFunctionDetail(context.Background(), "public", "f")
	if err == nil {
		t.Fatal("expected error from LoadFunctionDetail")
	}
	if _, ok := h.FunctionDetail("public", "f"); ok {
		t.Fatal("cache populated despite load error")
	}
}

func TestConnectHelperFunctionDetailDeepCopiesArgs(t *testing.T) {
	want := []models.FunctionDetail{{
		Schema: "public",
		Name:   "f",
		Args: []models.FunctionArg{
			{Name: "a", Type: "int4", Mode: "IN"},
		},
	}}
	sess := &fakeSession{describeFuncOut: want}
	h := connectFakeFuncDetail(t, "fake-fd-deepcopy", sess)

	if _, err := h.LoadFunctionDetail(context.Background(), "public", "f"); err != nil {
		t.Fatalf("LoadFunctionDetail: %v", err)
	}

	first, ok := h.FunctionDetail("public", "f")
	if !ok {
		t.Fatal("FunctionDetail miss after successful load")
	}
	if len(first) != 1 || len(first[0].Args) != 1 {
		t.Fatalf("unexpected shape: %+v", first)
	}

	// Mutate an element in place AND grow the Args slice via append. With a
	// shallow copy the in-place write would alias the cache's backing array and
	// the append (within capacity) would clobber a neighboring slot.
	first[0].Args[0].Name = "MUTATED"
	first[0].Args = append(first[0].Args, models.FunctionArg{Name: "extra"})

	second, ok := h.FunctionDetail("public", "f")
	if !ok {
		t.Fatal("FunctionDetail miss on second read")
	}
	if len(second) != 1 || len(second[0].Args) != 1 {
		t.Fatalf("cache Args length mutated through returned slice: %+v", second)
	}
	if second[0].Args[0].Name != "a" {
		t.Fatalf("cache Args element mutated through returned slice: %q", second[0].Args[0].Name)
	}
}

func TestConnectHelperLoadFunctionDetailEmptyResultPopulatesKey(t *testing.T) {
	sess := &fakeSession{describeFuncOut: []models.FunctionDetail{}}
	h := connectFakeFuncDetail(t, "fake-fd-empty", sess)

	got, err := h.LoadFunctionDetail(context.Background(), "public", "f")
	if err != nil {
		t.Fatalf("LoadFunctionDetail: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("LoadFunctionDetail returned %v, want empty", got)
	}

	cached, ok := h.FunctionDetail("public", "f")
	if !ok {
		t.Fatal("empty successful load must populate the key (want found=true)")
	}
	if cached == nil {
		t.Fatal("empty hit must return a non-nil slice")
	}
	if len(cached) != 0 {
		t.Fatalf("cached = %v, want empty", cached)
	}
}

func TestConnectHelperWarmFunctionDetailFiresOnReadyOnce(t *testing.T) {
	want := []models.FunctionDetail{{Schema: "public", Name: "f"}}
	sess := &fakeSession{describeFuncOut: want}
	h := connectFakeFuncDetail(t, "fake-fd-warm", sess)

	// Record UI-loop scheduling: onReady must run via the injected scheduler.
	var uiCalls atomic.Int32
	h.SetUIScheduler(func(fn func() error) {
		uiCalls.Add(1)
		_ = fn()
	})

	var ready atomic.Int32
	done := make(chan struct{}, 1)
	h.WarmFunctionDetail("public", "f", func() {
		ready.Add(1)
		select {
		case done <- struct{}{}:
		default:
		}
	})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("onReady never fired after warm")
	}

	// Give any erroneous second invocation a chance to land.
	time.Sleep(20 * time.Millisecond)
	if got := ready.Load(); got != 1 {
		t.Fatalf("onReady fired %d times, want 1", got)
	}
	if got := uiCalls.Load(); got != 1 {
		t.Fatalf("UI scheduler invoked %d times, want 1", got)
	}
	if _, ok := h.FunctionDetail("public", "f"); !ok {
		t.Fatal("warm did not populate the cache")
	}
}

func TestConnectHelperWarmFunctionDetailNilOnReadySafe(t *testing.T) {
	sess := &fakeSession{describeFuncOut: []models.FunctionDetail{{Name: "f"}}}
	h := connectFakeFuncDetail(t, "fake-fd-warm-nil", sess)

	h.WarmFunctionDetail("public", "f", nil)

	// Poll for cache population — the warm runs async.
	deadline := time.After(2 * time.Second)
	for {
		if _, ok := h.FunctionDetail("public", "f"); ok {
			return
		}
		select {
		case <-deadline:
			t.Fatal("warm with nil onReady never populated cache")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestConnectHelperWarmFunctionDetailConcurrentSingleRoundTrip(t *testing.T) {
	// Block DescribeFunction until released so all warms pile up while one load
	// is in flight, exercising the in-flight dedup.
	release := make(chan struct{})
	sess := &fakeSession{describeFuncOut: []models.FunctionDetail{{Name: "f"}}}
	blocking := &blockingDescribeSession{fakeSession: sess, release: release}

	h := connectBlockingDescribe(t, "fake-fd-warm-conc", blocking)

	var ready atomic.Int32
	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			h.WarmFunctionDetail("public", "f", func() { ready.Add(1) })
		}()
	}

	// Let all warms race to claim the in-flight slot.
	time.Sleep(30 * time.Millisecond)
	close(release)
	wg.Wait()

	// Allow the single load + its onReady to settle.
	deadline := time.After(2 * time.Second)
	for {
		if _, ok := h.FunctionDetail("public", "f"); ok {
			break
		}
		select {
		case <-deadline:
			t.Fatal("cache never populated")
		case <-time.After(5 * time.Millisecond):
		}
	}
	time.Sleep(20 * time.Millisecond)

	if got := blocking.describeCalls.Load(); got != 1 {
		t.Fatalf("DescribeFunction round-trips = %d, want 1 (dedup broken)", got)
	}
	if got := ready.Load(); got != 1 {
		t.Fatalf("onReady fired %d times, want exactly 1", got)
	}
}

// blockingDescribeSession wraps fakeSession to block DescribeFunction on a
// channel and count round-trips independently, so the in-flight dedup test can
// hold a single load open while concurrent warms pile up.
type blockingDescribeSession struct {
	*fakeSession
	release       chan struct{}
	describeCalls atomic.Int32
}

func (s *blockingDescribeSession) DescribeFunction(ctx context.Context, _, _ string) ([]models.FunctionDetail, error) {
	s.describeCalls.Add(1)
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.describeFuncOut, nil
}

func connectBlockingDescribe(t *testing.T, name string, sess drivers.Session) *ConnectHelper {
	t.Helper()
	bc := &blockingConnection{sess: sess}
	drv := &blockingDriver{conn: bc}
	drivers.Register(name, func(_ context.Context) (drivers.Driver, error) {
		return drv, nil
	})
	h := NewConnectHelper()
	if _, _, err := h.Connect(context.Background(), &models.Connection{Name: "p", Driver: name}, nil); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(h.Disconnect)
	return h
}

// blockingConnection / blockingDriver return the blocking session wrapper from
// AcquireSession so DescribeFunction goes through the wrapper, not the embedded
// fakeSession.
type blockingConnection struct {
	sess   drivers.Session
	closed atomic.Bool
}

func (c *blockingConnection) Close() error                 { c.closed.Store(true); return nil }
func (c *blockingConnection) Ping(_ context.Context) error { return nil }
func (c *blockingConnection) ServerVersion() string        { return "fake-1.0" }
func (c *blockingConnection) AcquireSession(_ context.Context) (drivers.Session, error) {
	return c.sess, nil
}
func (c *blockingConnection) Cancel(_ context.Context, _ models.QueryID) error { return nil }

type blockingDriver struct{ conn *blockingConnection }

func (d *blockingDriver) Name() string                       { return "fake" }
func (d *blockingDriver) Capabilities() drivers.Capabilities { return drivers.Capabilities{} }
func (d *blockingDriver) Open(_ context.Context, _ drivers.ConnectionProfile, _ drivers.ProgressReporter) (drivers.Connection, error) {
	return d.conn, nil
}
