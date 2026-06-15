package orchestrator

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
)

// regression suite.
//
// Hazard: the completion engine runs on the schema-rail session (a distinct
// pooled conn) and is benign, but the FK-cache forward (gd) and reverse (gD
// picker) paths route through g.activeSQLSession.FKCache() — the SAME driver
// session a >initial-fill "parked" stream holds open via its inFlight guard.
// On a cache miss the loader (ListForeignKeys / ListInboundForeignKeys) calls
// the driver guard's acquireInFlight, which panics "session: concurrent use"
// while the parked stream still holds it.
//
// Fix: both paths call (*Gui).preemptForFKCacheLoad → ResultTabsHelper.
// PreemptInFlight before the loader, which Stop()s the parked stream so its
// stream closes and the guard releases first (last-wins, like run/explain).
//
// These tests faithfully reproduce the guard's panic semantics with a fake
// driver session and assert: (1) the adapter / reverse method release the
// guard before the loader runs, and (2) a negative control proving the loader
// genuinely panics when the guard is held without a preempt.

// fkGuardSession is a drivers.Session that mimics the pg driver's inFlight
// guard (pkg/drivers/pg/session.go acquireInFlight / guard): the FK loaders
// CAS the guard 0->1 and panic on contention, exactly as the real driver
// does. A held guard represents a parked stream's open portal.
type fkGuardSession struct {
	inFlight atomic.Int32
	fwd      []models.ForeignKey
	rev      []models.ForeignKey
}

// acquire mirrors (*pg.Session).acquireInFlight: panic on concurrent use.
func (s *fkGuardSession) acquire() {
	if !s.inFlight.CompareAndSwap(0, 1) {
		panic("session: concurrent use")
	}
}

func (s *fkGuardSession) release() { s.inFlight.Store(0) }

func (s *fkGuardSession) ListForeignKeys(context.Context, string, string) ([]models.ForeignKey, error) {
	s.acquire()
	defer s.release()
	return s.fwd, nil
}

func (s *fkGuardSession) ListInboundForeignKeys(context.Context, string, string) ([]models.ForeignKey, error) {
	s.acquire()
	defer s.release()
	return s.rev, nil
}

// Remaining drivers.Session surface: trivial stubs (unexercised by these
// tests). Stream is never reached — the parked guard is simulated directly
// via acquire() and the tab is opened with a nil RunHandle.
func (s *fkGuardSession) Close() error         { return nil }
func (s *fkGuardSession) ID() models.SessionID { return 1 }
func (s *fkGuardSession) ListDatabases(context.Context) ([]models.Database, error) {
	return nil, nil
}

func (s *fkGuardSession) ListSchemas(context.Context, string) ([]models.Schema, error) {
	return nil, nil
}

func (s *fkGuardSession) ListTables(context.Context, string) ([]*models.Table, error) {
	return nil, nil
}

func (s *fkGuardSession) ListColumns(context.Context, string, string) ([]models.Column, error) {
	return nil, nil
}

func (s *fkGuardSession) ListIndexes(context.Context, string, string) ([]models.Index, error) {
	return nil, nil
}

func (s *fkGuardSession) ListConstraints(context.Context, string, string) ([]models.Constraint, error) {
	return nil, nil
}

func (s *fkGuardSession) ListFunctions(context.Context) ([]string, error) { return nil, nil }

func (s *fkGuardSession) DescribeFunction(context.Context, string, string) ([]models.FunctionDetail, error) {
	return nil, nil
}

func (s *fkGuardSession) Execute(context.Context, models.Query) (models.Result, error) {
	return models.Result{}, nil
}

func (s *fkGuardSession) Stream(context.Context, models.Query) (drivers.RowStream, error) {
	return nil, nil
}

func (s *fkGuardSession) Explain(context.Context, models.Query, bool) (models.Plan, error) {
	return models.Plan{}, nil
}

func (s *fkGuardSession) Begin(context.Context, models.TxOptions) (drivers.Transaction, error) {
	return nil, nil
}
func (s *fkGuardSession) InTransaction() bool                     { return false }
func (s *fkGuardSession) CurrentTransaction() drivers.Transaction { return nil }
func (s *fkGuardSession) Encoder() drivers.Encoder                { return fkNopEncoder{} }

type fkNopEncoder struct{}

func (fkNopEncoder) EncodeLiteral(any, uint32) string { return "NULL" }

var _ drivers.Session = (*fkGuardSession)(nil)

// fkGuardConn is the drivers.Connection SQLSession wraps; all methods are
// stubs (none are reached on the FK-load path under test).
type fkGuardConn struct{}

func (*fkGuardConn) Close() error                                            { return nil }
func (*fkGuardConn) Ping(context.Context) error                              { return nil }
func (*fkGuardConn) ServerVersion() string                                   { return "fake" }
func (*fkGuardConn) AcquireSession(context.Context) (drivers.Session, error) { return nil, nil }
func (*fkGuardConn) Cancel(context.Context, models.QueryID) error            { return nil }

// fkStopRunner is a ui.StreamRunner whose Stop() runs onStop — used to model
// the production chain PreemptInFlight -> RBM.Stop -> stream.Close ->
// releaseInFlight. Every other method is a no-op: a tab opened with a nil
// RunHandle never invokes NewQueryTask / ReadRows / EstimatedRows.
type fkStopRunner struct {
	onStop func()
}

func (r *fkStopRunner) NewQueryTask(string, func(context.Context) (drivers.RowStream, error), func([]models.Row), int, func(error)) error {
	return nil
}

func (r *fkStopRunner) Stop() {
	if r.onStop != nil {
		r.onStop()
	}
}
func (r *fkStopRunner) ReadRows(int)           {}
func (r *fkStopRunner) ReadToEnd(then func())  {}
func (r *fkStopRunner) EstimatedRows() int64   { return 0 }
func (r *fkStopRunner) SetEstimatedRows(int64) {}

var _ ui.StreamRunner = (*fkStopRunner)(nil)

// newParkedFKFixture builds a Gui whose activeSQLSession wraps a guard-
// simulating fake driver session, with one StateRunning tab whose runner
// releases the guard on Stop(). The driver guard is held to represent a
// parked stream's open portal. A real PreemptInFlight on this tab releases
// the guard, mirroring production.
func newParkedFKFixture(t *testing.T, inner *fkGuardSession) *Gui {
	t.Helper()
	sess := session.New(&fkGuardConn{}, inner, session.Options{})
	tabs := ui.NewResultTabsHelper(ui.ResultTabsHelperDeps{
		StreamFactory: func() ui.StreamRunner {
			return &fkStopRunner{onStop: inner.release}
		},
	})
	// rh=nil opens the tab directly in StateRunning with the factory's
	// runner attached but without driving a real stream.
	if err := tabs.OpenResultTab("parked", nil); err != nil {
		t.Fatalf("OpenResultTab: %v", err)
	}
	// Simulate the parked stream holding the driver session's inFlight guard.
	inner.acquire()
	return &Gui{resultTabsH: tabs, queryState: queryState{activeSQLSession: sess}}
}

// TestFKForwardLoadPreemptsParkedStream proves the gd forward path
// (activeSessionFKCacheAdapter.Get) preempts the parked stream BEFORE the
// FK-cache loader touches the shared driver session, so the loader's guard
// acquire does not panic. Without preemptForFKCacheLoad this test panics
// with "session: concurrent use".
func TestFKForwardLoadPreemptsParkedStream(t *testing.T) {
	inner := &fkGuardSession{fwd: []models.ForeignKey{{Name: "orders_customer_fkey"}}}
	g := newParkedFKFixture(t, inner)
	adapter := &activeSessionFKCacheAdapter{g: g}

	fks, err := adapter.Get(context.Background(), "public", "orders")
	if err != nil {
		t.Fatalf("adapter.Get errored (preempt did not release the guard before the loader): %v", err)
	}
	if len(fks) != 1 || fks[0].Name != "orders_customer_fkey" {
		t.Fatalf("forward FKs = %+v, want one named orders_customer_fkey", fks)
	}
}

// TestFKReverseLoadPreemptsParkedStream proves the gD reverse path
// ((*Gui).lookupReverseFK, wired as ReverseFKLookup) does the same before
// GetReverse's ListInboundForeignKeys loader runs.
func TestFKReverseLoadPreemptsParkedStream(t *testing.T) {
	inner := &fkGuardSession{rev: []models.ForeignKey{{Name: "child_parent_fkey"}}}
	g := newParkedFKFixture(t, inner)

	fks, err := g.lookupReverseFK(context.Background(), "public", "orders")
	if err != nil {
		t.Fatalf("lookupReverseFK errored (preempt did not release the guard before the loader): %v", err)
	}
	if len(fks) != 1 || fks[0].Name != "child_parent_fkey" {
		t.Fatalf("reverse FKs = %+v, want one named child_parent_fkey", fks)
	}
}

// TestFKLoadWhileParkedPanicsWithoutPreempt is the negative control: calling
// the FK-cache loader directly (no preempt) while the guard is held panics
// with the driver's "session: concurrent use" — the exact bug this
// fixes. This proves the guard is real and that the preempt in the positive
// tests is what prevents the panic, not the fake being permissive.
func TestFKLoadWhileParkedPanicsWithoutPreempt(t *testing.T) {
	inner := &fkGuardSession{fwd: []models.ForeignKey{{Name: "x"}}}
	sess := session.New(&fkGuardConn{}, inner, session.Options{})
	fkc := sess.FKCache()
	inner.acquire() // parked stream holds the driver guard; no preempt follows

	defer func() {
		switch r := recover(); r {
		case "session: concurrent use":
			// expected
		case nil:
			t.Fatal("expected 'session: concurrent use' panic from the FK loader while the guard is held; got none")
		default:
			t.Fatalf("unexpected panic: %v", r)
		}
	}()
	_, _ = fkc.Get(context.Background(), "public", "orders")
}
