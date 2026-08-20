package orchestrator_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
)

// C7 stress half: Gui-level -race stress for the launch-during-connect and
// close-during-run classes. These compose a REAL wired Gui (buildTestGui /
// registerWireFake) with the runner-level launch queue and the C6 shutdown
// sequence, asserting the same bounds the freeze fixes promised: launches
// settle under Bind/Unbind churn (no race, no deadlock) and Close returns
// within the bounded-shutdown ceiling with a run in flight.

// churnStreamSession is the safe session the Bind/Unbind churn alternates
// with the nil binding. Unlike the shared fakeStreamSession (whose embedded
// drivers.Session is nil), it overrides CurrentTransaction so SQLSession.Close
// — which calls s.inner.CurrentTransaction() — cannot nil-deref.
type churnStreamSession struct {
	drivers.Session
}

func (churnStreamSession) ID() models.SessionID { return models.SessionID(1) }
func (churnStreamSession) Stream(context.Context, models.Query) (drivers.RowStream, error) {
	return eofRowStream{qid: models.QueryID{SessionID: 1, Nonce: 1}}, nil
}

func (churnStreamSession) CurrentTransaction() drivers.Transaction { return nil }

// Close is called by SQLSession.Close; overriding it keeps the nil-embedded
// drivers.Session from nil-derefing.
func (churnStreamSession) Close() error { return nil }

// TestStressLaunchDuringConnectBindChurn keeps the QueryRunner's atomic
// Bind/Unbind swap churning on one goroutine while 30 launches fire
// concurrently from other goroutines — the exact reconnect window a user
// can land an Enter inside. Every launch must settle within 2s with a
// settle-able outcome (clean, sentinel-cancelled, or no-session), and
// goleak must see a quiescent pool afterwards. Under -race a torn binding
// publication or a launch queued into a half-dropped session would trip
// the detector here.
func TestStressLaunchDuringConnectBindChurn(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	g, _ := buildTestGuiWithHistory(t)
	defer func() { _ = g.Close() }()
	bag := g.HelperBagForTest()

	caps := drivers.Capabilities{HasLiveCancel: true}
	driverName, _ := registerWireFake(t, caps)
	profile := &models.Connection{Name: "stress-churn", Driver: driverName, DSN: "postgres://stub"}

	// Real connect path via a directly-constructed ConnectHelper (the bag's
	// ConnectInvoker interface exposes no teardown seam, and Disconnect is
	// what joins the per-Session worker goroutine for a clean goleak exit).
	helper := data.NewConnectHelper()
	if _, _, err := helper.Connect(context.Background(), profile, nil); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// A safe session for the reconnect churn. Take it over IMMEDIATELY so the
	// wire-fake session (whose Stream returns nil) is never the live binding
	// during the launch window below.
	sess2 := session.New(fakeConn{}, churnStreamSession{}, session.Options{})
	bag.QueryRunner.Bind(sess2, caps)

	// churnStarted closes as the churn goroutine begins; launches below wait
	// for it so they only ever see the nil or sess2 binding — never the
	// wire-fake session with its nil-Stream stub.
	const churnIters = 100
	churnStarted := make(chan struct{})
	churnDone := make(chan struct{})
	go func() {
		defer close(churnDone)
		close(churnStarted)
		for i := 0; i < churnIters; i++ {
			bag.QueryRunner.Unbind()
			bag.QueryRunner.Bind(sess2, caps)
		}
	}()
	<-churnStarted

	// Launches race the churn: each must settle within 2s.
	const launches = 30
	var wg sync.WaitGroup
	wg.Add(launches)
	for i := 0; i < launches; i++ {
		go func() {
			defer wg.Done()
			bag.QueryRunner.RunAsync(context.Background(), "SELECT churn", data.RunOptions{}, func(rh *session.RunHandle, err error) {
				if rh != nil {
					_ = rh.Rows().Close()
				}
				switch {
				case err == nil,
					errors.Is(err, context.Canceled),
					errors.Is(err, data.ErrNoSession),
					errors.Is(err, session.ErrPreemptPending):
				default:
					t.Errorf("launch during churn err = %v, want a settle-able outcome", err)
				}
			})
		}()
	}

	allDone := make(chan struct{})
	go func() { wg.Wait(); close(allDone) }()
	select {
	case <-allDone:
	case <-time.After(2 * time.Second):
		t.Fatal("DEADLOCK: launches did not settle within 2s under Bind/Unbind churn")
	}
	<-churnDone

	// Post-churn sanity: a launch against the last bind succeeds.
	finalAck := make(chan error, 1)
	bag.QueryRunner.RunAsync(context.Background(), "SELECT final", data.RunOptions{}, func(rh *session.RunHandle, err error) {
		if rh != nil {
			_ = rh.Rows().Close()
		}
		finalAck <- err
	})
	select {
	case err := <-finalAck:
		if err != nil {
			t.Fatalf("post-churn launch err = %v, want nil (runner bound after churn)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("post-churn launch did not settle within 2s")
	}

	// Tear the connect worker down so the goroutine pool is quiescent for
	// goleak (ConnectHelper.Connect spawns runWorker, which only exits when
	// Disconnect closes its queue).
	helper.Disconnect()

	if err := sess2.Close(); err != nil {
		t.Fatalf("sess2 Close: %v", err)
	}
}

// gatedStressConn is the inert drivers.Connection for the close-during-run
// fixture (only Cancel is reachable via RunHandle.cancelFn).
type gatedStressConn struct{ drivers.Connection }

func (gatedStressConn) Cancel(context.Context, models.QueryID) error { return nil }

// gatedStressSession is a drivers.Session whose Stream parks inside the
// call (closing started, then blocking on release or ctx cancellation)
// until the test lets it through — the deterministic stand-in for a run
// genuinely in flight when Gui.Close fires. Its rows are a clean EOF, so
// the abandoned path closes them instantly on release.
type gatedStressSession struct {
	drivers.Session
	started chan struct{}
	release chan struct{}
}

func (s *gatedStressSession) ID() models.SessionID { return models.SessionID(42) }
func (s *gatedStressSession) Stream(ctx context.Context, _ models.Query) (drivers.RowStream, error) {
	close(s.started)
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return eofRowStream{qid: models.QueryID{SessionID: 42, Nonce: 1}}, nil
}

// CurrentTransaction and Close are called by SQLSession.Close — overriding
// them keeps the nil-embedded drivers.Session from nil-derefing.
func (s *gatedStressSession) CurrentTransaction() drivers.Transaction { return nil }
func (s *gatedStressSession) Close() error                            { return nil }

// TestStressCloseDuringRunBounded fires a launch whose op parks inside its
// Stream call and then calls Gui.Close with the run mid-flight. The C6
// bounded shutdown must return within the 5s ceiling (the launcher-idle
// wait expires and Close fails loud instead of hanging), and once the
// parked op is released the abandoned launch must settle and the pool go
// goleak-clean.
func TestStressCloseDuringRunBounded(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	g, _ := buildTestGui(t)
	defer func() { _ = g.Close() }()
	bag := g.HelperBagForTest()

	releaseGate := make(chan struct{})
	inner := &gatedStressSession{started: make(chan struct{}), release: releaseGate}
	sess := session.New(&gatedStressConn{}, inner, session.Options{})
	bag.QueryRunner.Bind(sess, drivers.Capabilities{HasLiveCancel: true})

	ack := make(chan struct{})
	bag.QueryRunner.RunAsync(context.Background(), "SELECT in-flight", data.RunOptions{}, func(_ *session.RunHandle, _ error) {
		close(ack)
	})
	// Prove the op is genuinely inside Stream before Close fires.
	<-inner.started

	start := time.Now()
	closeDone := make(chan error, 1)
	go func() { closeDone <- g.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("Close took %v — beyond the C6 bounded-shutdown ceiling", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("DEADLOCK: Close hung >5s with a run in flight")
	}

	// Release the parked op; the abandoned launch settles and the launcher
	// drains to idle.
	close(releaseGate)
	select {
	case <-ack:
	case <-time.After(2 * time.Second):
		t.Fatal("launch ack did not settle within 2s after Close")
	}
	g.WaitForWorkersForTest()

	if err := sess.Close(); err != nil {
		t.Fatalf("sess Close: %v", err)
	}
}
