//go:build integration

// Package orchestrator_test (integration) proves the running-query
// subtitle (pgsavvy-vky3.5) end-to-end against the live docker postgres
// fixture: through the shipped command handlers (commands.QueryRun /
// commands.HistoryOpen) the subtitle appears animated on the shared
// query_editor view while a pg_sleep run streams server-side, disappears
// at settle, never clobbers a list leaf's buffer while it owns the
// shared view, and persists without a blank frame through a rapid
// double-run (single-flight FIFO: the second launch last-wins-cancels
// the first, whose settle is a generation-stale no-op clear).
//
// Reuses the harness idioms of query_execution_smoke_integration_test.go
// (requireSmokePG / registerSmokeDriver / runCommand / seedEditor /
// eventuallyQE / waitForHistorySQL — same orchestrator_test package
// under the integration tag) and the unit file's subtitleDriver
// (query_editor_subtitle_test.go) so reads of the live view's Subtitle
// field serialise against the inline UpdateContentOnly closures the
// recorder executes on the posting goroutine. No helpers are redefined
// here; each test is self-contained: smoke-gating, live connect, Close,
// goleak.
package orchestrator_test

import (
	"context"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"go.uber.org/goleak"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/pgsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/query"
)

// runningSubtitleShapeRe matches status.BuildRunningSubtitle output for
// a sub-minute run: one braille glyph, a space, then FormatElapsed's
// "tenths of a second" form ("⠋ 0.0s" / "⠙ 2.4s").
var runningSubtitleShapeRe = regexp.MustCompile(`^\S \d+\.\ds$`)

// subtitleSmoke bundles the live components built during setup. Mirrors
// queryExecutionSmoke but drives a subtitleDriver (real query_editor
// view enabled + closure-serialized subtitle reads) instead of a bare
// recorder.
type subtitleSmoke struct {
	g           *orchestrator.Gui
	drv         *subtitleDriver
	dsn         string
	connID      string
	historyPath string
	connections []models.Connection
}

// setupSubtitleSmoke spins up a wired Gui with the subtitle-capable
// recorder driver and a per-test history.sqlite under t.TempDir()
// (requireSmokePG gates on PGSAVVY_TEST_PG; registerSmokeDriver
// registers the "postgres" factory exactly once per binary). The real
// query_editor view is enabled BEFORE the layout pass so the first
// SetView materialises the live *gocui.View the subtitle paints on. A
// defensive t.Cleanup Close keeps a mid-test Fatal from hanging the
// deferred worker drain; the explicit finish() Close is idempotent.
func setupSubtitleSmoke(t *testing.T) *subtitleSmoke {
	t.Helper()
	dsn := requireSmokePG(t)
	registerSmokeDriver()

	fs := afero.NewMemMapFs()
	log := slog.New(slog.DiscardHandler)
	cfg := config.GetDefaultConfig()
	tr := i18n.EnglishTranslationSet()
	c := common.NewCommon(log, tr, cfg, &common.AppState{}, fs)
	store := common.NewAppStateStore(fs, "/state/state.yml", common.DefaultClock())

	tmp := t.TempDir()
	historyPath := filepath.Join(tmp, "history.sqlite")

	s := &subtitleSmoke{
		dsn:         dsn,
		connID:      "subtitle-smoke",
		historyPath: historyPath,
	}
	s.drv = &subtitleDriver{statusRepaintDriver: &statusRepaintDriver{
		RecorderGuiDriver: testfake.NewRecorderGuiDriver(),
	}}
	s.drv.EnableRealView(guicontext.QueryRailViewName)

	s.g = orchestrator.NewGui(orchestrator.Deps{
		Common:              c,
		Store:               store,
		ConnectionsPath:     filepath.Join(tmp, "connections.yml"),
		ConnectionsProvider: func() []models.Connection { return s.connections },
		DriverNamesFn:       func() []string { return []string{"postgres"} },
		HistoryProvider: func() (*query.History, error) {
			return query.New(historyPath)
		},
	})
	if err := s.g.UseDriverForTest(s.drv); err != nil {
		t.Fatalf("UseDriverForTest: %v", err)
	}
	s.drv.SetManager(s.g)
	t.Cleanup(func() { _ = s.g.Close() })
	return s
}

// connect drives the real bag.Connect.Connect against the fixture so
// wireQueryRuntime fires (QueryRunner bound + busy bridge + query-run
// signal installed) — the shipped <leader>r path short-circuits with
// "no active connection" without it.
func (s *subtitleSmoke) connect(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	profile := models.Connection{
		Name:   s.connID,
		Driver: "postgres",
		DSN:    s.dsn,
	}
	s.connections = []models.Connection{profile}
	bag := s.g.HelperBagForTest()
	if bag.Connect == nil {
		t.Fatal("HelperBag.Connect is nil after wireWithDriver")
	}
	if err := bag.Connect.Connect(ctx, &profile); err != nil {
		t.Fatalf("bag.Connect.Connect: %v", err)
	}
	if !bag.QueryRunner.HasSession() {
		t.Fatal("QueryRunner.HasSession() = false after Connect; wireQueryRuntime did not Bind")
	}
}

// showRail pushes the QUERY_RAIL container as the top main context and
// runs one layout pass at the fixed geometry, materialising the shared
// query_editor view with the editor leaf active. Every later layout in
// these tests uses the same 80x24 so noteLayoutSize never flags a
// resize (which would divert a tick into a forced full layout).
func (s *subtitleSmoke) showRail(t *testing.T) {
	t.Helper()
	if err := s.g.ContextTree().Push(s.g.Registry().QueryRail); err != nil {
		t.Fatalf("Push(QueryRail): %v", err)
	}
	if err := s.g.RunLayout(80, 24); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	if s.drv.RealView(guicontext.QueryRailViewName) == nil {
		t.Fatal("query_editor real view did not materialize after RunLayout")
	}
	if got := s.g.Registry().QueryRail.ActiveLeafKey(); got != types.QUERY_EDITOR {
		t.Fatalf("ActiveLeafKey() = %q after layout, want QUERY_EDITOR", got)
	}
}

// finish closes every open result tab (parked RBM workers included),
// Closes the Gui, and goleak-checks — the capstone walkthrough's
// post-test invariants, applied per test since each is self-contained.
func (s *subtitleSmoke) finish(t *testing.T) {
	t.Helper()
	if helper := s.g.ResultTabsHelper(); helper != nil {
		for helper.Count() > 0 {
			if err := helper.CloseActive(); err != nil {
				t.Fatalf("CloseActive: %v", err)
			}
		}
	}
	if err := s.g.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	goleak.VerifyNone(t, goleak.IgnoreCurrent())
}

// TestRunningSubtitleAppearsAndClears pins the core epic observable
// against live Postgres through the shipped <leader>r handler: while a
// pg_sleep(3) streams, the shared query_editor view's Subtitle is
// non-empty, matches the status.BuildRunningSubtitle shape (glyph +
// elapsed tenths), and animates (two distinct frames) with the editor
// leaf active; once the run settles and the result tab opens, the
// subtitle is wiped and the run-state slot reports idle.
func TestRunningSubtitleAppearsAndClears(t *testing.T) {
	s := setupSubtitleSmoke(t)
	s.connect(t)
	s.showRail(t)
	helper := s.g.ResultTabsHelper()
	if helper == nil {
		t.Fatal("ResultTabsHelper not wired into orchestrator.Gui")
	}
	before := helper.Count()

	seedEditor(t, s.g, "SELECT pg_sleep(3)")
	runCommand(t, s.g, commands.QueryRun)

	// Visibility window: bounded poll while the run is in flight. The
	// set-site repaint lands inside the handler (recorder runs the
	// posted closure inline), the first spinner tick ~100ms later
	// advances the frame — two distinct well-shaped subtitles well
	// inside the ~3s server-side window.
	seen := map[string]bool{}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, ok := s.g.QueryRunStarted(); !ok {
			t.Fatal("run settled before the subtitle visibility window closed")
		}
		if got := s.g.Registry().QueryRail.ActiveLeafKey(); got != types.QUERY_EDITOR {
			t.Fatalf("ActiveLeafKey() = %q during the visibility window, want QUERY_EDITOR", got)
		}
		sub := s.drv.subtitleOf(guicontext.QueryRailViewName)
		if sub != "" {
			if !runningSubtitleShapeRe.MatchString(sub) {
				t.Fatalf("subtitle %q does not match the BuildRunningSubtitle shape (glyph + space + tenths)", sub)
			}
			seen[sub] = true
			if len(seen) >= 2 {
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	if len(seen) < 2 {
		t.Fatalf("subtitle did not animate in flight: %d distinct frames seen (%v)", len(seen), seen)
	}

	// Settle: pg_sleep(3) + drain; 15s is comfortably above the ~3s
	// statement. The result tab opens in settleStatement BEFORE the
	// run-scope clear fires, so at idle both hold: no tab delta missing,
	// no subtitle residue.
	if !eventuallyQE(t, 15*time.Second, func() bool {
		_, _, ok := s.g.QueryRunStarted()
		return !ok
	}) {
		t.Fatal("run never settled within 15s")
	}
	if !eventuallyQE(t, 2*time.Second, func() bool {
		return s.drv.subtitleOf(guicontext.QueryRailViewName) == ""
	}) {
		t.Fatalf("subtitle = %q after settle, want empty", s.drv.subtitleOf(guicontext.QueryRailViewName))
	}
	if got := helper.Count(); got != before+1 {
		t.Fatalf("tab count = %d after <leader>r settle, want %d", got, before+1)
	}

	s.finish(t)
}

// TestBusyHistoryLeafSubtitleNotClobbered pins the pre-mortem blocker
// end-to-end: while a real query-driven busy hold keeps the spinner
// ticker armed, the HISTORY list leaf owns the shared query_editor view
// and ≥2 ticks pass — the tick repaint must not write the subtitle and
// must not re-render editor content into the shared view. The rail
// buffer keeps the history list render (seeded by an earlier settled
// run) and carries none of the in-flight editor SQL.
func TestBusyHistoryLeafSubtitleNotClobbered(t *testing.T) {
	s := setupSubtitleSmoke(t)
	s.connect(t)
	s.showRail(t)

	// Seed one settled history row with a highlighter-inert marker (no
	// SQL keywords — passes through unstyled, mirroring the unit-file
	// marker discipline) so the History leaf's reload has content to
	// render into the shared view.
	const histMarker = "zzqqlongmarker_hist"
	seedEditor(t, s.g, "SELECT 424242 AS "+histMarker)
	runCommand(t, s.g, commands.QueryRun)
	if !eventuallyQE(t, 15*time.Second, func() bool {
		_, _, ok := s.g.QueryRunStarted()
		return !ok
	}) {
		t.Fatal("marker run never settled within 15s")
	}
	if !waitForHistorySQL(t, s.historyPath, histMarker, 2*time.Second) {
		t.Fatalf("history row for %q never flushed", histMarker)
	}

	// Switch the rail to the HISTORY leaf through the shipped command:
	// the leaf's focus hook reloads (stale since the run above), and a
	// layout pass at the same geometry paints the list body into the
	// shared view.
	runCommand(t, s.g, commands.HistoryOpen)
	if got := s.g.Registry().QueryRail.ActiveLeafKey(); got != types.HISTORY {
		t.Fatalf("ActiveLeafKey() = %q after history.open, want HISTORY", got)
	}
	if !eventuallyQE(t, 3*time.Second, func() bool {
		return len(s.g.Registry().History.Items()) >= 1
	}) {
		t.Fatal("HISTORY leaf did not reload the recorded row within 3s")
	}
	if err := s.g.RunLayout(80, 24); err != nil {
		t.Fatalf("RunLayout(history): %v", err)
	}
	if buf := s.drv.GetViewBuffer(guicontext.QueryRailViewName); !strings.Contains(buf, histMarker) {
		t.Fatalf("pre-condition: rail buffer does not carry the history render; buf=%q", buf)
	}
	if got := s.drv.subtitleOf(guicontext.QueryRailViewName); got != "" {
		t.Fatalf("pre-condition: subtitle = %q on the history leaf, want empty", got)
	}

	// Busy window: seed the editor with the in-flight SQL and run it
	// while the HISTORY leaf still owns the shared view. The busy hold
	// (execLaunch's HoldBusy bridge) arms the ticker for the whole
	// stream, so a ~300ms real-time window covers ≥2 spinner ticks
	// (every tick unconditionally repaints the status line — the
	// statusWrites delta is the deterministic tick proof).
	baseline := s.drv.statusWrites.Load()
	const busyMarker = "pg_sleep"
	seedEditor(t, s.g, "SELECT pg_sleep(3)")
	runCommand(t, s.g, commands.QueryRun)
	if _, _, ok := s.g.QueryRunStarted(); !ok {
		t.Fatal("run not in flight immediately after <leader>r")
	}
	time.Sleep(300 * time.Millisecond)

	if _, _, ok := s.g.QueryRunStarted(); !ok {
		t.Fatal("run settled before the tick-observation window closed")
	}
	if delta := s.drv.statusWrites.Load() - baseline; delta < 2 {
		t.Fatalf("only %d status-line repaints during the busy window, want >= 2 (spinner ticks did not land)", delta)
	}
	if got := s.drv.subtitleOf(guicontext.QueryRailViewName); got != "" {
		t.Fatalf("subtitle = %q on the history leaf during busy ticks, want empty", got)
	}
	buf := s.drv.GetViewBuffer(guicontext.QueryRailViewName)
	if !strings.Contains(buf, histMarker) {
		t.Fatalf("rail buffer lost the history render during busy ticks; buf=%q", buf)
	}
	if strings.Contains(buf, busyMarker) {
		t.Fatalf("rail buffer carries the in-flight editor SQL during busy ticks; buf=%q", buf)
	}
	if real := s.drv.realViewBufferOf(guicontext.QueryRailViewName); strings.Contains(real, busyMarker) {
		t.Fatalf("real view carries the in-flight editor SQL during busy ticks; buf=%q", real)
	}

	// Settle before teardown so Close never races a live stream.
	if !eventuallyQE(t, 15*time.Second, func() bool {
		_, _, ok := s.g.QueryRunStarted()
		return !ok
	}) {
		t.Fatal("busy run never settled within 15s")
	}

	s.finish(t)
}

// TestRapidDoubleRunSubtitlePersists pins the double-run contract: two
// confirmed <leader>r launches within 100ms (the second last-wins-cancels
// the first per the single-flight queue; the first is abandoned + wire-cancelled
// at the session so the second runs immediately, and settles as a
// generation-stale no-op clear). From launch until the second run's
// settle, a tight poll must never observe an empty subtitle while the
// run state reports in flight, at least two distinct frames must be
// observed (animation proof), and after the final settle the subtitle
// is empty and the surviving run's result tab is intact.
func TestRapidDoubleRunSubtitlePersists(t *testing.T) {
	s := setupSubtitleSmoke(t)
	s.connect(t)
	s.showRail(t)
	helper := s.g.ResultTabsHelper()
	if helper == nil {
		t.Fatal("ResultTabsHelper not wired into orchestrator.Gui")
	}

	seedEditor(t, s.g, "SELECT pg_sleep(2)")
	launch := time.Now()
	runCommand(t, s.g, commands.QueryRun)
	runCommand(t, s.g, commands.QueryRun)
	if d := time.Since(launch); d > 100*time.Millisecond {
		t.Fatalf("two back-to-back <leader>r launches took %vms, want < 100ms (handlers must enqueue and return)", d.Milliseconds())
	}

	// Tight continuous poll (~50ms) until the second run settles. Read
	// order is load-bearing: the subtitle first, the state second.
	// ClearQueryRun clears the state BEFORE posting the wipe, so an
	// empty subtitle with a still-running state is a real defect, never
	// a settle-order artifact.
	distinct := map[string]bool{}
	deadline := time.Now().Add(15 * time.Second)
	for {
		sub := s.drv.subtitleOf(guicontext.QueryRailViewName)
		if _, _, ok := s.g.QueryRunStarted(); !ok {
			break
		}
		if sub == "" {
			t.Fatalf("subtitle observed empty while the run state reports in flight (%v after launch)", time.Since(launch))
		}
		distinct[sub] = true
		if time.Now().After(deadline) {
			t.Fatal("second run never settled within 15s")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(distinct) < 2 {
		t.Fatalf("subtitle did not animate across the double run: %d distinct frames (%v)", len(distinct), distinct)
	}

	// Final settle: the first launch surfaces nothing (cancelled); the
	// second opens exactly one tab and completes with a row.
	if !eventuallyQE(t, 5*time.Second, func() bool {
		return helper.Count() == 1
	}) {
		t.Fatalf("tab count = %d after the double run, want 1 (cancelled launch opens no tab)", helper.Count())
	}
	active := helper.Active()
	if active == nil {
		t.Fatal("Active() = nil after the double run")
	}
	if !eventuallyQE(t, 5*time.Second, func() bool {
		return active.RowCount() >= 1
	}) {
		t.Fatalf("surviving tab never delivered rows; state=%v rows=%d err=%v", active.State(), active.RowCount(), active.Err())
	}
	if err := active.Err(); err != nil {
		t.Fatalf("surviving tab Err = %v, want nil", err)
	}
	if !eventuallyQE(t, 2*time.Second, func() bool {
		return s.drv.subtitleOf(guicontext.QueryRailViewName) == ""
	}) {
		t.Fatalf("subtitle = %q after final settle, want empty", s.drv.subtitleOf(guicontext.QueryRailViewName))
	}

	s.finish(t)
}
