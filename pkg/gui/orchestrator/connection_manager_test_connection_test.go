package orchestrator_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/pgsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// TestConnectionManagerTestConnectionDoesNotTouchActiveSession is the headline
// architectural guarantee: pressing `t` to test the in-progress form dials via
// the decoupled drivers.Get → Open primitive and must NOT establish the real
// session, must NOT set the active connection, and must NOT pop the modal.
func TestConnectionManagerTestConnectionDoesNotTouchActiveSession(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	driverName, conn := registerWireFake(t, drivers.Capabilities{})
	conn.schemas = []models.Schema{{Name: "public"}}

	reg := g.Registry()
	modal := reg.ConnectionManager
	tree := g.ContextTree()

	if err := tree.Push(modal); err != nil {
		t.Fatalf("push modal: %v", err)
	}
	// Open an in-progress add form carrying the fake driver.
	modal.OpenAddForm(nil, func() []string { return []string{driverName} })

	// `t` → test the in-progress connection.
	if err := g.Controllers().ConnectionManager.TestConnection(commands.ExecCtx{}); err != nil {
		t.Fatalf("TestConnection: %v", err)
	}
	g.WaitForWorkersForTest()

	if got := g.ActiveConnIDForTest(); got != "" {
		t.Fatalf("active connection set by a test dial: %q; want untouched (empty)", got)
	}
	if modal.Mode() != guicontext.ModeForm {
		t.Fatalf("modal mode after test = %v, want ModeForm (test must not pop/switch)", modal.Mode())
	}
	if top := tree.Current(); top == nil || top.GetKey() != types.CONNECTION_MANAGER {
		t.Fatalf("modal popped by a test dial; want it to stay top")
	}
}

// TestConnectionManagerTestConnectionFailureStaysInForm asserts a dial FAILURE
// surfaces inline (form stays in ModeForm) without setting the active
// connection — the failure path is fully isolated from the live session.
func TestConnectionManagerTestConnectionFailureStaysInForm(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	driverName, conn := registerWireFake(t, drivers.Capabilities{})
	conn.openErr = errors.New("connection refused")

	reg := g.Registry()
	modal := reg.ConnectionManager
	tree := g.ContextTree()

	if err := tree.Push(modal); err != nil {
		t.Fatalf("push modal: %v", err)
	}
	modal.OpenAddForm(nil, func() []string { return []string{driverName} })

	if err := g.Controllers().ConnectionManager.TestConnection(commands.ExecCtx{}); err != nil {
		t.Fatalf("TestConnection: %v", err)
	}
	g.WaitForWorkersForTest()

	if got := g.ActiveConnIDForTest(); got != "" {
		t.Fatalf("active connection set by a failed test dial: %q; want empty", got)
	}
	if modal.Mode() != guicontext.ModeForm {
		t.Fatalf("modal mode after failed test = %v, want ModeForm", modal.Mode())
	}
}

// TestConnectionManagerTestConnectionSupersededPublishesNothing locks the
// stale-gen guard (AC6): when a SECOND `t` press supersedes the first, the
// first (superseded) dial's late publish MUST be dropped — the form's inline
// result reflects ONLY the winning second dial.
//
// To actually prove the guard fires (and would fail if testStale were deleted)
// the dial ordering is controlled so the SUPERSEDED publish lands LAST:
//
//	press t  → dial #1 starts, BLOCKS on a gate (testGen == 1)
//	press t  → dial #2 starts (testGen bumped to 2, supersedes #1)
//	dial #2 completes → publishes PASS "connection ok" (testGen == 2, fresh)
//	release dial #1 → it returns a FAILURE and publishes (testGen == 1, STALE)
//
// Dial #1's publish, if the testStale(1) guard were removed, would overwrite
// the form with the failure line. The assertion that the body shows "connection
// ok" and NOT the first dial's failure is therefore the red-green lock on the
// guard. The fake Open's openErr field is shared, so the two outcomes are
// sequenced via the gate: dial #2 reads openErr==nil (PASS) and completes
// before the test sets openErr and releases dial #1 (FAIL).
func TestConnectionManagerTestConnectionSupersededPublishesNothing(t *testing.T) {
	g, rec := buildTestGuiWithHistory(t)

	driverName, conn := registerWireFake(t, drivers.Capabilities{})
	conn.schemas = []models.Schema{{Name: "public"}}

	// Sequencing channels: gate releases the blocked first dial; started1
	// confirms dial #1 has entered Open and is parked on the gate.
	var dialN atomic.Int32
	gate := make(chan struct{})
	started1 := make(chan struct{})
	conn.openHookCtx = func(_ context.Context) {
		if dialN.Add(1) == 1 {
			close(started1)
			<-gate // park the superseded dial until the winner has published
		}
	}

	reg := g.Registry()
	modal := reg.ConnectionManager
	tree := g.ContextTree()

	// Register the modal view so HandleRender → SetContent → GetViewBuffer
	// captures the inline result body (mirrors the real layout's SetView).
	_, _ = rec.SetView(string(types.CONNECTION_MANAGER), 0, 0, 60, 20, 0)

	if err := tree.Push(modal); err != nil {
		t.Fatalf("push modal: %v", err)
	}
	modal.OpenAddForm(nil, func() []string { return []string{driverName} })

	ctrl := g.Controllers().ConnectionManager

	// Press #1: testGen → 1, dial #1 starts and blocks on the gate.
	if err := ctrl.TestConnection(commands.ExecCtx{}); err != nil {
		t.Fatalf("TestConnection #1: %v", err)
	}
	<-started1 // dial #1 is now parked

	// While dial #1 is parked the busy counter sits at 1 (its worker is the
	// only one in flight).
	if got := g.BusyCount(); got != 1 {
		t.Fatalf("busy count with dial #1 parked = %d, want 1", got)
	}

	// Press #2: testGen → 2 (supersedes #1). openErr is still nil, so dial #2
	// reads it as a PASS and publishes "connection ok" while it is the fresh gen.
	if err := ctrl.TestConnection(commands.ExecCtx{}); err != nil {
		t.Fatalf("TestConnection #2: %v", err)
	}
	// Wait (via the atomic busy counter, NOT by rendering) for dial #2's worker
	// to finish: its publish runs inside the worker, sequenced-before the
	// decrement, so observing the counter fall back to 1 establishes
	// happens-before with the form write while dial #1 stays parked (no
	// concurrent writer). Rendering here instead would race the live publish.
	waitForBusyCount(t, g, 1)

	// Dial #2 (the winner) has now stamped its PASS. Render on the test
	// goroutine — only the parked dial #1 remains and it has not published, so
	// there is no concurrent writer.
	if body := renderFormBody(t, modal, rec); !strings.Contains(body, "connection ok") {
		t.Fatalf("winning dial did not stamp PASS; body=\n%s", body)
	}

	// Now make the SUPERSEDED dial #1 fail with a distinguishable outcome and
	// release it (the channel close synchronizes the openErr write with dial
	// #1's read in Open). Its publish runs with the stale gen (1) and must be
	// dropped by testStale.
	conn.openErr = errors.New("superseded-first-result")
	close(gate)
	g.WaitForWorkersForTest()

	// Render the form and assert it shows ONLY the winning dial's PASS — never
	// the superseded dial #1's failure. Deleting the testStale(gen) guard in
	// publishTestResult makes dial #1's late publish stamp the failure here,
	// failing this assertion (the red-green lock on the guard).
	body := renderFormBody(t, modal, rec)
	if !strings.Contains(body, "connection ok") {
		t.Fatalf("form lost the winning dial's PASS; body=\n%s", body)
	}
	if strings.Contains(body, "superseded-first-result") {
		t.Fatalf("superseded dial #1 publish overwrote the form — testStale guard not firing; body=\n%s", body)
	}

	if got := g.ActiveConnIDForTest(); got != "" {
		t.Fatalf("active connection set during superseded test: %q; want empty", got)
	}
	if modal.Mode() != guicontext.ModeForm {
		t.Fatalf("modal mode after superseded test = %v, want ModeForm", modal.Mode())
	}
}

// waitForBusyCount blocks until the Gui's live OnWorker busy counter equals want
// or a short deadline elapses. The counter is atomic, so observing the target
// value establishes happens-before with everything the finished worker did
// before its decrement — used here to know a publish completed without
// rendering (which would race the live worker writing the form).
func waitForBusyCount(t *testing.T, g *orchestrator.Gui, want int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if g.BusyCount() == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for busy count to reach %d; got %d", want, g.BusyCount())
}

// renderFormBody renders the connection-manager modal once on the calling
// goroutine and returns the captured inline body. MUST be called only when no
// worker is concurrently writing the form (the publish and HandleRender both
// touch form.status, which is single-threaded on the real MainLoop).
func renderFormBody(t *testing.T, modal *guicontext.ConnectionManagerContext, rec *testfake.RecorderGuiDriver) string {
	t.Helper()
	if err := modal.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	return rec.GetViewBuffer(string(types.CONNECTION_MANAGER))
}
