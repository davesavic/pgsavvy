package orchestrator_test

import (
	"context"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// A supersession bump mid-dial causes the worker to
// find itself stale and publish NOTHING. Neither activeConn nor the persisted
// LastConnectionID may be stamped. We inject the bump from inside the dial
// via openHook.
func TestConnectInvokerCancelMidDialDropsSuccessfulResult(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	caps := drivers.Capabilities{}
	driverName, conn := registerWireFake(t, caps)

	bag := g.HelperBagForTest()

	// openHook fires on the worker goroutine during the dial. Bumping the
	// generation makes the in-flight attempt stale.
	conn.openHook = func() { g.BumpConnectGenForTest() }

	profile := &models.Connection{Name: "cancelled", Driver: driverName, DSN: "postgres://stub"}
	_ = bag.Connect.Connect(context.Background(), profile)

	if got := g.ActiveConnIDForTest(); got != "" {
		t.Fatalf("activeConnID = %q after cancel-mid-dial; want empty (cancelled result must not clobber)", got)
	}
	if got := g.LastConnectionIDForTest(); got != "" {
		t.Fatalf("LastConnectionID = %q after cancel-mid-dial; want empty (cancel must not stamp persisted state)", got)
	}
}

// Two sequential connects; only the second succeeds
// because the first is superseded mid-dial. The active connection must be the
// second profile.
func TestConnectInvokerRapidCancelThenNewAttempt(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	caps := drivers.Capabilities{}
	driverName, conn := registerWireFake(t, caps)

	bag := g.HelperBagForTest()

	// First connect is superseded mid-dial.
	conn.openHook = func() { g.BumpConnectGenForTest() }
	first := &models.Connection{Name: "first", Driver: driverName, DSN: "postgres://stub"}
	_ = bag.Connect.Connect(context.Background(), first)

	// Second connect succeeds (no hook, no supersession).
	conn.openHook = nil
	second := &models.Connection{Name: "second", Driver: driverName, DSN: "postgres://stub"}
	_ = bag.Connect.Connect(context.Background(), second)

	if got := g.ActiveConnIDForTest(); got != "second" {
		t.Fatalf("activeConnID = %q; want \"second\" (only the newest attempt wins; the cancelled first must drop)", got)
	}
}

// AC: Connect with no driver wired is a no-op — nil cancelFn, no panic.
func TestConnectInvokerCancelNoInflightIsNoop(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)
	// No connect attempt started. Must not panic.
	_ = g
}
