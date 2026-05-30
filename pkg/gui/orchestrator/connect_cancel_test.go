package orchestrator_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/models"
	"go.uber.org/goleak"
)

// AC epic dbsavvy-e53.5: a Cancel issued mid-dial bumps connectGen strictly
// above the in-flight attempt's gen, so the worker — even though its dial
// SUCCEEDS — finds itself stale and publishes NOTHING. Neither activeConn nor
// the persisted LastConnectionID may be stamped (the cancel-after-dial-success
// side-effect gate). We inject the cancel from inside the dial via openHook.
func TestConnectInvokerCancelMidDialDropsSuccessfulResult(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	caps := drivers.Capabilities{}
	driverName, conn := registerWireFake(t, caps)

	// Capture ONE bag so OnBeginConnecting (startAttempt) and OnCancelConnecting
	// (Cancel) operate on the SAME connectInvoker instance.
	bag := g.HelperBagForTest()

	// openHook fires on the worker goroutine during the dial. Cancel bumps
	// connectGen above the attempt's gen → the dial that follows is stale.
	conn.openHook = func() { bag.OnCancelConnecting() }

	profile := &models.Connection{Name: "cancelled", Driver: driverName, DSN: "postgres://stub"}
	bag.OnBeginConnecting(profile)

	// startAttempt dispatched the dial on a worker goroutine; wait for it.
	g.WaitForWorkersForTest()

	if got := g.ActiveConnIDForTest(); got != "" {
		t.Fatalf("activeConnID = %q after cancel-mid-dial; want empty (cancelled result must not clobber)", got)
	}
	if got := g.LastConnectionIDForTest(); got != "" {
		t.Fatalf("LastConnectionID = %q after cancel-mid-dial; want empty (cancel must not stamp persisted state)", got)
	}
}

// AC epic dbsavvy-e53.5: rapid Esc→Enter — two attempts with a Cancel between.
// Only the newest attempt's gen wins; the older (cancelled) attempt's publish
// is dropped. We block the first dial until the second attempt has bumped the
// gen, then release it and assert the active connection is the SECOND profile.
func TestConnectInvokerRapidCancelThenNewAttempt(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	caps := drivers.Capabilities{}
	driverName, conn := registerWireFake(t, caps)

	bag := g.HelperBagForTest()

	release := make(chan struct{})
	var dialN atomic.Int32
	// Only the FIRST dial blocks (until released); later dials proceed. The
	// hook field is set once and never mutated, so no race with the worker
	// goroutine reading it under -race.
	conn.openHook = func() {
		if dialN.Add(1) == 1 {
			<-release
		}
	}

	first := &models.Connection{Name: "first", Driver: driverName, DSN: "postgres://stub"}
	bag.OnBeginConnecting(first)

	// Cancel the first (bumps gen + cancels its ctx), then start the second.
	bag.OnCancelConnecting()

	second := &models.Connection{Name: "second", Driver: driverName, DSN: "postgres://stub"}
	bag.OnBeginConnecting(second)

	// Release the first (now-superseded) dial so its worker can finish and
	// find itself stale.
	close(release)

	g.WaitForWorkersForTest()

	if got := g.ActiveConnIDForTest(); got != "second" {
		t.Fatalf("activeConnID = %q; want \"second\" (only the newest attempt wins; the cancelled first must drop)", got)
	}
}

// AC epic dbsavvy-e53.5: Cancel with no in-flight attempt is a no-op — nil
// cancelFn, no panic.
func TestConnectInvokerCancelNoInflightIsNoop(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	bag := g.HelperBagForTest()
	// No OnBeginConnecting → cancelFn is nil. Must not panic.
	bag.OnCancelConnecting()
	bag.OnCancelConnecting()
}

// AC epic dbsavvy-e53.5 (BLOCKER, goleak/Close-bounded): an unresponsive host
// whose Open blocks on <-ctx.Done() must unblock when Cancel aborts the dial,
// so the worker exits and Gui.Close() returns within a bounded time. Confirms
// the cancellable ctx threads to the driver's Open.
func TestConnectInvokerCancelUnblocksDialAndCloseReturns(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	g, _ := buildTestGuiWithHistory(t)

	caps := drivers.Capabilities{}
	driverName, conn := registerWireFake(t, caps)

	dialEntered := make(chan struct{})
	// Block the dial on the cancellable ctx (unresponsive host). If the ctx
	// does NOT thread through to Open, this never unblocks and Close hangs.
	conn.openHookCtx = func(ctx context.Context) {
		close(dialEntered)
		<-ctx.Done()
	}

	bag := g.HelperBagForTest()
	profile := &models.Connection{Name: "unresponsive", Driver: driverName, DSN: "postgres://stub"}
	bag.OnBeginConnecting(profile)

	// Wait until the dial is actually parked inside Open.
	select {
	case <-dialEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("dial never entered Open within 5s")
	}

	// Cancel aborts the ctx → Open's <-ctx.Done() unblocks → the worker exits.
	bag.OnCancelConnecting()

	g.WaitForWorkersForTest()

	done := make(chan error, 1)
	go func() { done <- g.Close() }()
	select {
	case <-done:
		// Close returned — the cancelled worker did not wedge shutdown.
	case <-time.After(5 * time.Second):
		t.Fatal("Gui.Close() did not return within 5s after cancelling a blocked dial")
	}
}
