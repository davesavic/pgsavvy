package data

import (
	"context"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// This file covers the C4/C6 quit-during-ANALYZE safety: a quit that lands
// inside a runner-owned EXPLAIN ANALYZE BEGIN..ROLLBACK wrap must never
// commit the wrap tx.

// gatedAnalyzeSession embeds sequencedSession (Begin/Explain/Rollback
// recording) but gates the Explain call so a test can park an ANALYZE op
// INSIDE its BEGIN..ROLLBACK wrap — the exact state a quit can land in.
type gatedAnalyzeSession struct {
	*sequencedSession
	gate chan struct{}
}

func (g *gatedAnalyzeSession) Explain(ctx context.Context, q models.Query, analyze bool) (models.Plan, error) {
	select {
	case <-g.gate:
	case <-ctx.Done():
		return models.Plan{}, ctx.Err()
	}
	return g.sequencedSession.Explain(ctx, q, analyze)
}

// TestQuitDuringAnalyzeNeverCommitsWrapTx is C6 gap #4: a quit landing
// inside an in-flight EXPLAIN ANALYZE wrap must not commit the
// runner-owned transaction. CancelAndWaitActiveRun reports the op as
// non-drainable, WrapTxOpen() exposes the wrap, and the wrap tx is rolled
// back — never committed — when the op finally resolves.
func TestQuitDuringAnalyzeNeverCommitsWrapTx(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	inner := &sequencedSession{}
	gate := make(chan struct{})
	fs := &gatedAnalyzeSession{sequencedSession: inner, gate: gate}
	r := NewQueryRunner(fs, drivers.Capabilities{})

	r.ExplainAsync(context.Background(), "INSERT INTO t VALUES (1)", true, "", func(models.Plan, error) {})
	// Wait for the wrap BEGIN — the op is past the queued-cancel fence.
	deadline := time.After(2 * time.Second)
	for len(fs.snapshot()) == 0 {
		select {
		case <-deadline:
			t.Fatal("ExplainAsync op never started")
		case <-time.After(time.Millisecond):
		}
	}
	tx, _ := fs.CurrentTransaction().(*seqTx)
	if tx == nil || !fs.InTransaction() {
		t.Fatalf("wrap tx not open during in-flight ANALYZE (tx=%v)", tx)
	}

	// The quit drain cannot interrupt the in-flight op: it expires and
	// reports the Explain as non-drainable.
	if nonDrainable := r.CancelAndWaitActiveRun(); !nonDrainable {
		t.Fatal("CancelAndWaitActiveRun did not report the in-flight Explain as non-drainable")
	}
	if !r.WrapTxOpen() {
		t.Fatal("WrapTxOpen() = false while the ANALYZE wrap is still open, want true")
	}

	// commitAndQuit gates on WrapTxOpen(): it must skip the commit. Assert
	// the runner-owned wrap tx is never committed and is rolled back once
	// the op resolves.
	close(gate)
	waitRolledBack := time.After(2 * time.Second)
	for fs.InTransaction() {
		select {
		case <-waitRolledBack:
			t.Fatal("wrap never rolled back after the op resolved")
		case <-time.After(time.Millisecond):
		}
	}
	if tx.committed.Load() {
		t.Fatal("runner-owned ANALYZE wrap tx was committed — quit must skip it")
	}
	if !tx.rolledBack.Load() {
		t.Fatal("runner-owned ANALYZE wrap tx was not rolled back")
	}
}

// TestCancelQueuedAndWaitIdleTimeoutExpiresWithStuckOp is the bounded part
// of the close-time drain: an in-flight op that never resolves (the C4
// Explain kind — no RunHandle, detached from its sentinel ctx) cannot hang
// Close. The bound fires and the error is returned instead.
func TestCancelQueuedAndWaitIdleTimeoutExpiresWithStuckOp(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	fs := &gatedAnalyzeSession{sequencedSession: &sequencedSession{}, gate: make(chan struct{})}
	r := NewQueryRunner(fs, drivers.Capabilities{})

	done := make(chan struct{})
	r.ExplainAsync(context.Background(), "SELECT 1", true, "", func(models.Plan, error) { close(done) })
	// Wait for the wrap to begin — the op is now parked inside the Explain
	// call (gated).
	deadline := time.After(2 * time.Second)
	for len(fs.snapshot()) == 0 {
		select {
		case <-deadline:
			t.Fatal("ExplainAsync op never started")
		case <-time.After(time.Millisecond):
		}
	}

	start := time.Now()
	err := r.CancelQueuedAndWaitIdle(100 * time.Millisecond)
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("drain took %v, want ~100ms bound (op is non-drainable)", elapsed)
	}
	if err == nil {
		t.Fatal("drain on a stuck Explain op returned nil, want error")
	}
	// Release the op (the parked Explain call) so it resolves and the
	// goroutines settle.
	close(fs.gate)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Explain op never acked after release")
	}
}
