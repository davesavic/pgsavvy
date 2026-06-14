package orchestrator_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// T3 staged connect-progress wiring: the modal/Retry dial
// path threads a LIVE ProgressReporter into the driver and renders a staged
// checklist (Tunnel? → Auth → Objects) inside the CONNECTION_MANAGER modal.
// These tests assert the rendered checklist for each acceptance criterion.
//
// They read the staged ConnectingState body via BodyGlyph(' ') (a static glyph
// keeps the assertion deterministic). popModalOnSuccess only flips the mode +
// pops the modal — it does NOT clear the stage list — so the terminal frame is
// still observable on ConnectingState after WaitForWorkersForTest.

// modalStagedBody returns the rendered staged checklist body for assertions.
// A fixed glyph keeps the Active-row rendering deterministic across frames.
func modalStagedBody(t *testing.T, modal *guicontext.ConnectionManagerContext) string {
	t.Helper()
	return modal.ConnectingState().BodyGlyph(' ')
}

// TestStagedConnect_TunnellessShowsAuthAndObjects asserts a tunnel-less connect
// renders the [Authenticated, Loading objects] checklist and ends on a visible
// "✓ Loaded N schemas" row — and never an SSH-tunnel line.
func TestStagedConnect_TunnellessShowsAuthAndObjects(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	driverName, conn := registerWireFake(t, drivers.Capabilities{})
	conn.schemas = []models.Schema{{Name: "public", Owner: "u"}, {Name: "app", Owner: "u"}}
	conn.emitStages = []drivers.ConnectStage{drivers.StageAuthenticated}

	modal := g.Registry().ConnectionManager
	profile := &models.Connection{Name: "tunnelless", Driver: driverName, DSN: "postgres://stub"}
	if err := g.ContextTree().Push(modal); err != nil {
		t.Fatalf("push modal: %v", err)
	}
	modal.SetItems([]any{profile})

	if err := g.Controllers().ConnectionManager.Confirm(commands.ExecCtx{}); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	g.WaitForWorkersForTest()

	body := modalStagedBody(t, modal)
	if strings.Contains(body, "SSH tunnel") {
		t.Errorf("tunnel-less connect rendered an SSH-tunnel line:\n%s", body)
	}
	if !strings.Contains(body, "✓ Authenticated") {
		t.Errorf("missing '✓ Authenticated' row:\n%s", body)
	}
	if !strings.Contains(body, "✓ Loaded 2 schemas") {
		t.Errorf("missing '✓ Loaded 2 schemas' row:\n%s", body)
	}
}

// TestStagedConnect_ConditionalTunnel asserts a profile carrying an SSH tunnel
// renders a Tunnel row BEFORE Auth, both marked done by the emitted stages.
func TestStagedConnect_ConditionalTunnel(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	driverName, conn := registerWireFake(t, drivers.Capabilities{})
	conn.schemas = []models.Schema{{Name: "public", Owner: "u"}}
	conn.emitStages = []drivers.ConnectStage{drivers.StageTunnel, drivers.StageAuthenticated}

	modal := g.Registry().ConnectionManager
	profile := &models.Connection{
		Name:      "tunneled",
		Driver:    driverName,
		DSN:       "postgres://stub",
		SSHTunnel: &models.SSHTunnelConfig{Host: "bastion", User: "deploy", Port: 22},
	}
	if err := g.ContextTree().Push(modal); err != nil {
		t.Fatalf("push modal: %v", err)
	}
	modal.SetItems([]any{profile})

	if err := g.Controllers().ConnectionManager.Confirm(commands.ExecCtx{}); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	g.WaitForWorkersForTest()

	body := modalStagedBody(t, modal)
	tunnelIdx := strings.Index(body, "SSH tunnel")
	authIdx := strings.Index(body, "Authenticated")
	if tunnelIdx < 0 {
		t.Fatalf("tunneled connect missing SSH tunnel row:\n%s", body)
	}
	if authIdx < 0 || tunnelIdx > authIdx {
		t.Fatalf("Tunnel row must precede Auth row:\n%s", body)
	}
	if !strings.Contains(body, "✓ SSH tunnel") {
		t.Errorf("Tunnel row not marked done:\n%s", body)
	}
	if !strings.Contains(body, "✓ Authenticated") {
		t.Errorf("Auth row not marked done:\n%s", body)
	}
}

// TestStagedConnect_EmptySchemaList asserts an empty schema list renders
// "✓ Loaded 0 schemas".
func TestStagedConnect_EmptySchemaList(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	driverName, conn := registerWireFake(t, drivers.Capabilities{})
	conn.schemas = nil
	conn.emitStages = []drivers.ConnectStage{drivers.StageAuthenticated}

	modal := g.Registry().ConnectionManager
	profile := &models.Connection{Name: "empty-schemas", Driver: driverName, DSN: "postgres://stub"}
	if err := g.ContextTree().Push(modal); err != nil {
		t.Fatalf("push modal: %v", err)
	}
	modal.SetItems([]any{profile})

	if err := g.Controllers().ConnectionManager.Confirm(commands.ExecCtx{}); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	g.WaitForWorkersForTest()

	body := modalStagedBody(t, modal)
	if !strings.Contains(body, "✓ Loaded 0 schemas") {
		t.Errorf("missing '✓ Loaded 0 schemas' row:\n%s", body)
	}
}

// TestStagedConnect_SchemaLoadFailureMarksObjectsFailed asserts a ListSchemas
// error fails the Objects row (✗) while the connect itself STILL SUCCEEDS with
// an empty rail (no error phase entered — IsError stays false).
func TestStagedConnect_SchemaLoadFailureMarksObjectsFailed(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	driverName, conn := registerWireFake(t, drivers.Capabilities{})
	conn.schemasErr = errors.New("ListSchemas: boom")
	conn.emitStages = []drivers.ConnectStage{drivers.StageAuthenticated}

	modal := g.Registry().ConnectionManager
	profile := &models.Connection{Name: "schema-fail", Driver: driverName, DSN: "postgres://stub"}
	if err := g.ContextTree().Push(modal); err != nil {
		t.Fatalf("push modal: %v", err)
	}
	modal.SetItems([]any{profile})

	if err := g.Controllers().ConnectionManager.Confirm(commands.ExecCtx{}); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	g.WaitForWorkersForTest()

	// Connect still succeeds: active connection set, modal popped.
	if got := g.ActiveConnIDForTest(); got != "schema-fail" {
		t.Fatalf("activeConnID = %q after schema-load failure; want schema-fail (connect must still succeed)", got)
	}
	cs := modal.ConnectingState()
	if cs.IsError() {
		t.Errorf("IsError() = true after schema-load failure; want false (connect succeeds with empty rail)")
	}
	body := cs.BodyGlyph(' ')
	if !strings.Contains(body, "✗ Loading objects…") {
		t.Errorf("Objects row not marked failed:\n%s", body)
	}
}

// TestStagedConnect_SchemaLoadTimeoutMarksObjectsFailed asserts a HUNG
// ListSchemas trips the Objects-stage timeout budget (T3 AD7): the Objects row
// goes ✗ and the connect STILL completes (active conn set), instead of hanging
// forever on "Loading objects…".
func TestStagedConnect_SchemaLoadTimeoutMarksObjectsFailed(t *testing.T) {
	restore := orchestrator.SetSchemaLoadTimeoutForTest(20 * time.Millisecond)
	defer restore()

	g, _ := buildTestGuiWithHistory(t)

	driverName, conn := registerWireFake(t, drivers.Capabilities{})
	// Block ListSchemas forever; the timeout ctx must cancel it.
	conn.schemasBlock = make(chan struct{})
	conn.emitStages = []drivers.ConnectStage{drivers.StageAuthenticated}

	modal := g.Registry().ConnectionManager
	profile := &models.Connection{Name: "schema-timeout", Driver: driverName, DSN: "postgres://stub"}
	if err := g.ContextTree().Push(modal); err != nil {
		t.Fatalf("push modal: %v", err)
	}
	modal.SetItems([]any{profile})

	if err := g.Controllers().ConnectionManager.Confirm(commands.ExecCtx{}); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	g.WaitForWorkersForTest()

	if got := g.ActiveConnIDForTest(); got != "schema-timeout" {
		t.Fatalf("activeConnID = %q after schema timeout; want schema-timeout (connect must still complete)", got)
	}
	cs := modal.ConnectingState()
	if cs.IsError() {
		t.Errorf("IsError() = true after schema timeout; want false")
	}
	if body := cs.BodyGlyph(' '); !strings.Contains(body, "✗ Loading objects…") {
		t.Errorf("Objects row not marked failed after timeout:\n%s", body)
	}
}

// TestStagedConnect_SupersessionDropsStaleReports asserts that when attempt A is
// mid-dial and a newer activation (B) supersedes it, A's later reporter calls
// are gen-dropped INSIDE the marshalling closure and never mutate the modal's
// stages. We block A in Open, bump the connectGen (simulating B), release A,
// and assert A's StageAuthenticated emit produced no '✓ Authenticated' row.
func TestStagedConnect_SupersessionDropsStaleReports(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	driverName, conn := registerWireFake(t, drivers.Capabilities{})
	conn.schemas = []models.Schema{{Name: "public", Owner: "u"}}
	conn.emitStages = []drivers.ConnectStage{drivers.StageAuthenticated}
	// openHook fires on the worker during A's dial, BEFORE stages are emitted
	// (Open emits stages after openHook). Bump the gen here to supersede A.
	conn.openHook = func() { g.BumpConnectGenForTest() }

	modal := g.Registry().ConnectionManager
	profile := &models.Connection{Name: "superseded-A", Driver: driverName, DSN: "postgres://stub"}
	if err := g.ContextTree().Push(modal); err != nil {
		t.Fatalf("push modal: %v", err)
	}
	modal.SetItems([]any{profile})

	if err := g.Controllers().ConnectionManager.Confirm(commands.ExecCtx{}); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	g.WaitForWorkersForTest()

	body := modalStagedBody(t, modal)
	// A's StageAuthenticated Report was gen-dropped, so Auth stays Pending
	// (the seeded Active first-stage), never flipped to '✓ Authenticated'.
	if strings.Contains(body, "✓ Authenticated") {
		t.Errorf("stale attempt A's Report mutated the modal (saw '✓ Authenticated'):\n%s", body)
	}
	// And the superseded attempt set no active connection.
	if got := g.ActiveConnIDForTest(); got != "" {
		t.Errorf("activeConnID = %q after superseded connect; want empty", got)
	}
}

// TestStagedConnect_RetryAfterFailureFreshChecklist asserts that retrying after
// a dial failure replaces the checklist (no stale ✗ rows): the second attempt
// re-seeds via SetConnectingStaged and succeeds with a clean "✓ Loaded" row.
func TestStagedConnect_RetryAfterFailureFreshChecklist(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	driverName, conn := registerWireFake(t, drivers.Capabilities{})
	conn.schemas = []models.Schema{{Name: "public", Owner: "u"}}
	conn.emitStages = []drivers.ConnectStage{drivers.StageAuthenticated}
	conn.openErr = errors.New("dial failed: connection refused")

	modal := g.Registry().ConnectionManager
	profile := &models.Connection{Name: "retry-pg", Driver: driverName, DSN: "postgres://stub"}
	if err := g.ContextTree().Push(modal); err != nil {
		t.Fatalf("push modal: %v", err)
	}
	modal.SetItems([]any{profile})

	// First attempt fails — error phase, Auth row failed.
	if err := g.Controllers().ConnectionManager.Confirm(commands.ExecCtx{}); err != nil {
		t.Fatalf("Confirm (attempt 1): %v", err)
	}
	g.WaitForWorkersForTest()
	if !modal.ConnectingState().IsError() {
		t.Fatalf("attempt 1 did not enter error phase")
	}

	// Clear the dial error and retry via the controller's Retry seam.
	conn.openErr = nil
	if err := g.Controllers().ConnectionManager.Confirm(commands.ExecCtx{}); err != nil {
		t.Fatalf("Confirm (retry): %v", err)
	}
	g.WaitForWorkersForTest()

	if got := g.ActiveConnIDForTest(); got != "retry-pg" {
		t.Fatalf("activeConnID = %q after retry; want retry-pg", got)
	}
	body := modalStagedBody(t, modal)
	if strings.Contains(body, "✗") {
		t.Errorf("retry left a stale ✗ row in the fresh checklist:\n%s", body)
	}
	if !strings.Contains(body, "✓ Loaded 1 schema") {
		t.Errorf("retry success row missing '✓ Loaded 1 schema':\n%s", body)
	}
}

// TestStagedConnect_DirectPathNoStageWrites asserts the direct / reconnect path
// (connectInvoker.Connect, nil reporter) performs NO stage writes and does not
// panic — the checklist stays empty.
func TestStagedConnect_DirectPathNoStageWrites(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	driverName, conn := registerWireFake(t, drivers.Capabilities{})
	conn.schemas = []models.Schema{{Name: "public", Owner: "u"}}
	conn.emitStages = []drivers.ConnectStage{drivers.StageTunnel, drivers.StageAuthenticated}

	modal := g.Registry().ConnectionManager
	// Seed the modal with a staged (but inert) checklist so we can detect any
	// spurious mutation by the direct path.
	modal.ConnectingState().SetConnectingStaged("seed", []guicontext.Stage{
		{ID: guicontext.StageAuth, Label: "Authenticated"},
		{ID: guicontext.StageObjects, Label: "Loading objects…"},
	})

	bag := g.HelperBagForTest()
	profile := &models.Connection{Name: "direct-pg", Driver: driverName, DSN: "postgres://stub"}
	if err := bag.Connect.Connect(context.Background(), profile); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	g.WaitForWorkersForTest()

	// The direct path passes a nil reporter, so no stage transitions fire:
	// the seeded checklist is untouched (no ✓/✗ rows appeared).
	body := modal.ConnectingState().BodyGlyph(' ')
	if strings.Contains(body, "✓") || strings.Contains(body, "✗") {
		t.Errorf("direct path wrote stage transitions (nil reporter must be inert):\n%s", body)
	}
	if got := g.ActiveConnIDForTest(); got != "direct-pg" {
		t.Fatalf("activeConnID = %q; want direct-pg", got)
	}
}

// TestStagedConnect_ObjectsNotActivatedOnAuthBoundary asserts Objects is
// activated LATE (just before the schema load), NOT on the auth boundary (T3
// AD3): a clean connect must never render a spurious "✗ Loading objects…". The
// reporter's StageAuthenticated handler marks ONLY Auth done; a regression that
// also activated Objects on that boundary and then hit a failure in the
// post-auth gap would mis-render the ✗ here.
func TestStagedConnect_ObjectsNotActivatedOnAuthBoundary(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	driverName, conn := registerWireFake(t, drivers.Capabilities{})
	conn.schemas = []models.Schema{{Name: "public", Owner: "u"}}
	conn.emitStages = []drivers.ConnectStage{drivers.StageAuthenticated}

	modal := g.Registry().ConnectionManager
	profile := &models.Connection{Name: "late-objects", Driver: driverName, DSN: "postgres://stub"}
	if err := g.ContextTree().Push(modal); err != nil {
		t.Fatalf("push modal: %v", err)
	}
	modal.SetItems([]any{profile})

	if err := g.Controllers().ConnectionManager.Confirm(commands.ExecCtx{}); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	g.WaitForWorkersForTest()

	// On the StageAuthenticated boundary the reporter marks ONLY Auth done; it
	// must NOT activate Objects. By the time we read it, the load has run and
	// Objects is Done — but the key invariant (Objects not flipped Active by
	// the auth Report) is proven by the success path producing a clean
	// "✓ Loaded" with no spurious failure. A regression that activated Objects
	// on the auth boundary AND then failed in the gap would show ✗ here.
	body := modalStagedBody(t, modal)
	if strings.Contains(body, "✗ Loading objects…") {
		t.Errorf("clean connect rendered a spurious '✗ Loading objects…':\n%s", body)
	}
	if !strings.Contains(body, "✓ Loaded 1 schema") {
		t.Errorf("missing '✓ Loaded 1 schema':\n%s", body)
	}
}
