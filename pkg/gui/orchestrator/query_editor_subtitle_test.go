package orchestrator_test

import (
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/pgsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/pgsavvy/pkg/gui/status"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/query"
)

// This file covers the running-query subtitle on the shared query-rail
// view (pgsavvy-vky3.4): the animated "glyph + elapsed" border subtitle
// while a query run is in flight, driven off the spinner tick, the
// set/clear sites, and the Tier-1.4 layout branches.

// subtitleDriver extends statusRepaintDriver with a mutex that
// serializes UpdateContentOnly closures — the path the spinner-tick
// drain goroutine and the set/clear sites post through — against the
// test's reads of the REAL query_editor view's Subtitle/buffer fields.
// The recorder executes closures inline on the posting goroutine, so
// without this serialization reading v.Subtitle while the drain
// goroutine repaints would be a data race under -race; production has
// no such race because everything runs on the single gocui MainLoop.
// Update (full) is deliberately NOT wrapped: nesting a full Update
// inside a content-only closure (the resize-forced-layout tick path)
// would self-deadlock a non-reentrant mutex, and no test here resizes
// mid-busy.
type subtitleDriver struct {
	*statusRepaintDriver
	mu sync.Mutex
}

func (d *subtitleDriver) UpdateContentOnly(fn func() error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.statusRepaintDriver.UpdateContentOnly(fn)
}

// subtitleOf reads the live view's Subtitle field under the closure
// serialization mutex (mirrors the production MainLoop invariant that
// the field is only touched on the UI thread).
func (d *subtitleDriver) subtitleOf(name string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	v := d.RealView(name)
	if v == nil {
		return ""
	}
	return v.Subtitle
}

// realViewBufferOf reads the live view's rendered buffer under the same
// serialization.
func (d *subtitleDriver) realViewBufferOf(name string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	v := d.RealView(name)
	if v == nil {
		return ""
	}
	return v.ViewBuffer()
}

// cursorOf reads the live view's cursor position under the same
// serialization. The cursor is the observable for whether a repaint
// re-rendered the editor content: paintQueryEditorLeaf ends in
// FocusPoint, which overwrites (cx, cy) with the buffer cursor — so a
// cursor the test moved away stays moved ONLY if no re-render ran.
func (d *subtitleDriver) cursorOf(name string) (int, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	v := d.RealView(name)
	if v == nil {
		return 0, 0
	}
	return v.Cursor()
}

// setViewCursorForTest moves the live view's cursor under the same
// serialization.
func (d *subtitleDriver) setViewCursorForTest(name string, x, y int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if v := d.RealView(name); v != nil {
		v.SetCursor(x, y)
	}
}

// newSubtitleGui builds the standard fixture for the subtitle tests: a
// real query_editor view (EnableRealView), a fake clock, and the rail
// pushed as the active main context with the editor leaf active and one
// layout pass already run so the view exists. The history store is a
// per-test temp sqlite (not the developer's real XDG history DB): the
// HISTORY leaf re-highlights every row it renders, and a machine-local
// row count would make the resize-forced layouts slow and the fixture
// non-hermetic.
func newSubtitleGui(t *testing.T) (*orchestrator.Gui, *subtitleDriver, *fakeClock) {
	t.Helper()
	clk := newFakeClock()
	drv := &subtitleDriver{statusRepaintDriver: &statusRepaintDriver{RecorderGuiDriver: testfake.NewRecorderGuiDriver()}}
	drv.EnableRealView(guicontext.QueryRailViewName)

	fs := afero.NewMemMapFs()
	cfg := config.GetDefaultConfig()
	c := common.NewCommon(slog.New(slog.DiscardHandler), i18n.EnglishTranslationSet(), cfg, &common.AppState{}, fs)
	store := common.NewAppStateStore(fs, "/tmp/state.yml", common.DefaultClock())
	g := orchestrator.NewGui(orchestrator.Deps{
		Common:              c,
		Store:               store,
		ConnectionsPath:     "/tmp/connections.yml",
		ConnectionsProvider: func() []models.Connection { return nil },
		DriverNamesFn:       func() []string { return []string{"postgres"} },
		HistoryProvider: func() (*query.History, error) {
			return query.New(filepath.Join(t.TempDir(), "history.sqlite"))
		},
	}, orchestrator.WithClock(clk))
	if err := g.UseDriverForTest(drv); err != nil {
		t.Fatalf("UseDriverForTest: %v", err)
	}

	if err := g.ContextTree().Push(g.Registry().QueryRail); err != nil {
		t.Fatalf("Push(QueryRail): %v", err)
	}
	if err := drv.SetSize(80, 24); err != nil {
		t.Fatalf("SetSize: %v", err)
	}
	return g, drv, clk
}

// holdBusyForTest arms the busy counter (and thereby the spinner
// ticker) via the launcher-bridge path HoldBusy/ReleaseBusy — the same
// seam QueryRunner.execLaunch uses for the whole async run. The
// returned release is idempotent and doubles as the test cleanup along
// with Close.
func holdBusyForTest(t *testing.T, g *orchestrator.Gui) (release func()) {
	t.Helper()
	if !g.HoldBusy() {
		t.Fatal("HoldBusy returned false on an open Gui")
	}
	var once sync.Once
	release = func() { once.Do(g.ReleaseBusy) }
	t.Cleanup(func() {
		release()
		_ = g.Close()
	})
	return release
}

// waitForSubtitle polls the live subtitle until cond holds or a 1s
// deadline elapses (the drain goroutine lands its repaint shortly after
// tickAll; mirrors waitForStatusBuffer). Returns the satisfying value.
func waitForSubtitle(t *testing.T, drv *subtitleDriver, what string, cond func(string) bool) string {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		sub := drv.subtitleOf(guicontext.QueryRailViewName)
		if cond(sub) {
			return sub
		}
		select {
		case <-deadline:
			t.Fatalf("subtitle never satisfied %q; last=%q", what, sub)
		case <-time.After(time.Millisecond):
		}
	}
}

// waitStatusWrites waits until at least n further writes landed on the
// AppStatus view. Every spinner tick unconditionally repaints the
// status line, so this is the deterministic "the drain goroutine has
// processed the ticks" signal — independent of unrelated content-only
// posts (e.g. the history leaf's async reload).
func waitStatusWrites(t *testing.T, drv *subtitleDriver, baseline int64, n int64) {
	t.Helper()
	deadline := time.After(time.Second)
	for drv.statusWrites.Load()-baseline < n {
		select {
		case <-deadline:
			t.Fatalf("only %d status writes since baseline %d, want >= %d (spinner ticks did not land)",
				drv.statusWrites.Load()-baseline, baseline, n)
		case <-time.After(time.Millisecond):
		}
	}
}

// settleRailBuffer waits until the rail view's recorder buffer stops
// changing (absorbs the history leaf's async first-activation reload),
// then returns the settled content.
func settleRailBuffer(drv *subtitleDriver, d time.Duration) string {
	deadline := time.After(d)
	for {
		b1 := drv.GetViewBuffer(guicontext.QueryRailViewName)
		time.Sleep(30 * time.Millisecond)
		b2 := drv.GetViewBuffer(guicontext.QueryRailViewName)
		if b1 == b2 {
			return b2
		}
		select {
		case <-deadline:
			return b2
		default:
		}
	}
}

// typeEditorSQL seeds distinctive SQL into the canonical QUERY_EDITOR
// buffer through the VimEditor (insert mode), so a regression that
// paints editor content while a list leaf owns the shared view leaves
// the marker discoverable in the view buffer. The marker deliberately
// avoids SQL keywords so the highlighter passes it through unstyled.
func typeEditorSQL(t *testing.T, g *orchestrator.Gui, sqlText string) {
	t.Helper()
	ed := g.MasterEditorForTest(types.QUERY_EDITOR)
	if ed == nil {
		t.Fatal("no master editor wired for QUERY_EDITOR")
	}
	ed.Edit(nil, gocui.NewKeyRune('i'))
	for _, r := range sqlText {
		ed.Edit(nil, gocui.NewKeyRune(r))
	}
}

// TestQueryEditorSubtitleTickAnimates pins the core AC: with the editor
// leaf active and a run in flight, each spinner tick advances the
// subtitle — two DIFFERENT non-empty strings, each exactly
// status.BuildRunningSubtitle(frame, elapsed) for that tick's fake
// clock reading.
func TestQueryEditorSubtitleTickAnimates(t *testing.T) {
	g, drv, clk := newSubtitleGui(t)
	release := holdBusyForTest(t, g)

	g.SetQueryRunStarted("r1")
	// Set-site immediate repaint: the fake clock is frozen at the Set
	// stamp, so frame 0 / elapsed 0.
	first := waitForSubtitle(t, drv, "set-site paint", func(s string) bool { return s != "" })
	if want := status.BuildRunningSubtitle(0, 0); first != want {
		t.Fatalf("set-site subtitle = %q, want %q", first, want)
	}

	var subs [2]string
	for i := 1; i <= 2; i++ {
		clk.Advance(100 * time.Millisecond)
		clk.tickAll()
		want := status.BuildRunningSubtitle(int64(i), time.Duration(i)*100*time.Millisecond)
		subs[i-1] = waitForSubtitle(t, drv, "tick paint", func(s string) bool { return s == want })
		if subs[i-1] != want {
			t.Fatalf("tick %d subtitle = %q, want %q", i, subs[i-1], want)
		}
	}
	if subs[0] == "" || subs[1] == "" || subs[0] == subs[1] {
		t.Fatalf("subtitle did not animate over two ticks: %q then %q", subs[0], subs[1])
	}
	release()
}

// TestQueryEditorSubtitleSettleClearsWithoutTicker pins the settle path:
// once the busy hold is released (ticker disarmed) the forced clear
// posted by ClearQueryRun wipes the subtitle with no ticker running,
// and it stays wiped.
func TestQueryEditorSubtitleSettleClearsWithoutTicker(t *testing.T) {
	g, drv, _ := newSubtitleGui(t)
	release := holdBusyForTest(t, g)

	g.SetQueryRunStarted("r1")
	waitForSubtitle(t, drv, "set-site paint", func(s string) bool { return s != "" })

	release()
	g.ClearQueryRun("r1")

	if got := drv.subtitleOf(guicontext.QueryRailViewName); got != "" {
		t.Fatalf("subtitle = %q after settle clear, want empty", got)
	}
	// Ticker disarmed: nothing repaints the subtitle back.
	time.Sleep(50 * time.Millisecond)
	if got := drv.subtitleOf(guicontext.QueryRailViewName); got != "" {
		t.Fatalf("subtitle = %q after settle window, want empty", got)
	}
	if _, _, ok := g.QueryRunStarted(); ok {
		t.Fatal("run still reported in flight after ClearQueryRun")
	}
}

// TestQueryEditorSubtitleLeafSwitchClears pins the Tier-1.4 non-editor
// branch: switching the rail to a list leaf and running a full layout
// pass clears the subtitle (no linger on the shared view's border) and
// the buffer carries no editor SQL text.
func TestQueryEditorSubtitleLeafSwitchClears(t *testing.T) {
	g, drv, _ := newSubtitleGui(t)
	holdBusyForTest(t, g)
	const marker = "zzqqlongmarker_sql"
	typeEditorSQL(t, g, marker)

	g.SetQueryRunStarted("r1")
	waitForSubtitle(t, drv, "set-site paint", func(s string) bool { return s != "" })

	g.Registry().QueryRail.SetActiveTab(2) // History
	// Same geometry as the SetSize pass: no resize flag, so the ticks
	// below exercise the plain content-only repaint path.
	if err := g.RunLayout(80, 24); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	if got := drv.subtitleOf(guicontext.QueryRailViewName); got != "" {
		t.Fatalf("subtitle = %q after switching to a list leaf, want empty", got)
	}
	if buf := drv.GetViewBuffer(guicontext.QueryRailViewName); strings.Contains(buf, marker) {
		t.Fatalf("rail buffer carries editor SQL after leaf switch: %q", buf)
	}
}

// tickAndWait advances the fake clock by one spinner interval, fires
// one tick, and waits until that tick's status-line repaint landed (the
// per-tick drain confirmation). Waiting between ticks is required: the
// fake ticker channel is buffered 1, so a second back-to-back tickAll
// before the drain goroutine receives the first would be dropped.
func tickAndWait(t *testing.T, drv *subtitleDriver, clk *fakeClock, baseline int64, n int64) {
	t.Helper()
	clk.Advance(100 * time.Millisecond)
	clk.tickAll()
	waitStatusWrites(t, drv, baseline, n)
}

// TestQueryEditorSubtitleWrongLeafDuringBusyDoesNotClobber is the
// pre-mortem regression test: while a LIST leaf owns the shared view
// and the run is in flight with ticks flowing, the tick repaint must
// not write the subtitle AND must not re-render editor content into the
// shared view — the buffer stays byte-identical and marker-free.
func TestQueryEditorSubtitleWrongLeafDuringBusyDoesNotClobber(t *testing.T) {
	g, drv, clk := newSubtitleGui(t)
	g.Registry().QueryRail.SetActiveTab(2) // History owns the shared view
	// Same geometry as the SetSize pass: no resize flag, so the ticks
	// below exercise the plain content-only repaint path.
	if err := g.RunLayout(80, 24); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	const marker = "zzqqlongmarker_sql"
	typeEditorSQL(t, g, marker)

	holdBusyForTest(t, g)
	bufBefore := settleRailBuffer(drv, 300*time.Millisecond)

	g.SetQueryRunStarted("r1")
	baseline := drv.statusWrites.Load()
	tickAndWait(t, drv, clk, baseline, 1)
	tickAndWait(t, drv, clk, baseline, 2)
	time.Sleep(30 * time.Millisecond) // absorb any third repaint in flight

	if got := drv.subtitleOf(guicontext.QueryRailViewName); got != "" {
		t.Fatalf("subtitle = %q on a list leaf during busy, want empty (tick must not touch non-editor renders)", got)
	}
	if buf := drv.GetViewBuffer(guicontext.QueryRailViewName); buf != bufBefore {
		t.Fatalf("rail buffer changed during busy ticks on a list leaf: before=%q after=%q", bufBefore, buf)
	}
	if buf := drv.realViewBufferOf(guicontext.QueryRailViewName); strings.Contains(buf, marker) {
		t.Fatalf("real view carries editor SQL after busy ticks on a list leaf: %q", buf)
	}
}

// TestQueryEditorSubtitleViewMissingIsNoop pins the tolerance contract:
// with no real view behind the rail name, Set + ticks + Clear neither
// panic nor surface queued-closure errors.
func TestQueryEditorSubtitleViewMissingIsNoop(t *testing.T) {
	clk := newFakeClock()
	drv := &subtitleDriver{statusRepaintDriver: &statusRepaintDriver{RecorderGuiDriver: testfake.NewRecorderGuiDriver()}}
	g := buildTestGuiWithDriverAndClock(t, drv, clk)
	if err := g.ContextTree().Push(g.Registry().QueryRail); err != nil {
		t.Fatalf("Push(QueryRail): %v", err)
	}
	if err := drv.SetSize(80, 24); err != nil {
		t.Fatalf("SetSize: %v", err)
	}
	holdBusyForTest(t, g)

	g.SetQueryRunStarted("r1")
	baseline := drv.statusWrites.Load()
	tickAndWait(t, drv, clk, baseline, 1)
	tickAndWait(t, drv, clk, baseline, 2)
	g.ClearQueryRun("r1")

	if errs := drv.UpdateErrors(); len(errs) != 0 {
		t.Fatalf("queued Update closure errors: %v", errs)
	}
	if _, _, ok := g.QueryRunStarted(); ok {
		t.Fatal("run still in flight after ClearQueryRun")
	}
}

// TestQueryEditorSubtitleFastSettleClears pins the fast-settle path:
// Set immediately followed by Clear with zero ticks between — the
// set-site paint lands (recorder runs closures inline) and the forced
// clear wipes it in the same pump.
func TestQueryEditorSubtitleFastSettleClears(t *testing.T) {
	g, drv, _ := newSubtitleGui(t)

	g.SetQueryRunStarted("r1")
	if got := drv.subtitleOf(guicontext.QueryRailViewName); got == "" {
		t.Fatal("subtitle empty right after SetQueryRunStarted; the set-site repaint did not run")
	}
	g.ClearQueryRun("r1")
	if got := drv.subtitleOf(guicontext.QueryRailViewName); got != "" {
		t.Fatalf("subtitle = %q after fast settle, want empty", got)
	}
}

// TestQueryEditorSubtitleClearLandsUnderPromptOnTop pins the
// prompt-on-top requirement behaviorally: while a PROMPT popup owns the
// top of the focus stack (the state under which repaintBusyIndicators
// is suppressed), ClearQueryRun's forced wipe still lands on the
// subtitle. (Code-inspection counterpart: the clear path posts
// forceClearQueryRunSubtitle directly and never consults promptOnTop.)
func TestQueryEditorSubtitleClearLandsUnderPromptOnTop(t *testing.T) {
	g, drv, _ := newSubtitleGui(t)
	holdBusyForTest(t, g)

	g.SetQueryRunStarted("r1")
	waitForSubtitle(t, drv, "set-site paint", func(s string) bool { return s != "" })

	if err := g.ContextTree().Push(g.Registry().Prompt); err != nil {
		t.Fatalf("push prompt: %v", err)
	}
	g.ClearQueryRun("r1")
	if got := drv.subtitleOf(guicontext.QueryRailViewName); got != "" {
		t.Fatalf("subtitle = %q after clear under prompt-on-top, want empty", got)
	}
}

// TestQueryEditorSubtitleStaleClearKeepsNewerRun pins the
// generation-tag contract at the rendering layer: Set(A), Set(B),
// Clear(A) — the stale clear must not wipe B's subtitle, and the next
// tick paints B's frame built from B's startedAt.
func TestQueryEditorSubtitleStaleClearKeepsNewerRun(t *testing.T) {
	g, drv, clk := newSubtitleGui(t)
	holdBusyForTest(t, g)

	g.SetQueryRunStarted("run-a")
	waitForSubtitle(t, drv, "run-a set-site paint", func(s string) bool { return s != "" })

	clk.Advance(time.Second)
	g.SetQueryRunStarted("run-b")
	clk.Advance(700 * time.Millisecond)
	g.ClearQueryRun("run-a") // stale generation: no wipe may fire

	if got := drv.subtitleOf(guicontext.QueryRailViewName); got == "" {
		t.Fatal("stale clear wiped the newer run's subtitle")
	}
	clk.tickAll()
	want := status.BuildRunningSubtitle(7, 700*time.Millisecond)
	got := waitForSubtitle(t, drv, "run-b tick paint", func(s string) bool { return s == want })
	if got != want {
		t.Fatalf("subtitle = %q after stale clear + tick, want run-b's %q", got, want)
	}
}

// TestQueryEditorSubtitleReappearsOnLeafSwitchBack pins the reappear
// half of paintQueryRunSubtitleFromState: with a run in flight and the
// subtitle painted on the editor leaf, a leaf switch to HISTORY clears
// it (Tier-1.4 non-editor branch), and switching BACK to the editor
// leaf repainting it from LIVE state on the next full layout pass —
// exactly the string the advanced fake clock dictates, not a stale
// pre-switch frame.
func TestQueryEditorSubtitleReappearsOnLeafSwitchBack(t *testing.T) {
	g, drv, clk := newSubtitleGui(t)
	holdBusyForTest(t, g)

	g.SetQueryRunStarted("r1")
	waitForSubtitle(t, drv, "set-site paint", func(s string) bool { return s != "" })

	g.Registry().QueryRail.SetActiveTab(2) // History
	// Same geometry as the SetSize pass: no resize flag — this is the
	// plain Tier-1.4 branch, not a resize-forced layout.
	if err := g.RunLayout(80, 24); err != nil {
		t.Fatalf("RunLayout(history): %v", err)
	}
	if got := drv.subtitleOf(guicontext.QueryRailViewName); got != "" {
		t.Fatalf("pre-condition: subtitle = %q on the history leaf, want empty", got)
	}

	clk.Advance(350 * time.Millisecond)
	g.Registry().QueryRail.SetActiveTab(0) // back to the editor leaf
	if err := g.RunLayout(80, 24); err != nil {
		t.Fatalf("RunLayout(editor): %v", err)
	}

	want := status.BuildRunningSubtitle(3, 350*time.Millisecond)
	if got := drv.subtitleOf(guicontext.QueryRailViewName); got != want {
		t.Fatalf("subtitle = %q after switching back to the editor leaf, want live-state %q", got, want)
	}
}

// TestQueryEditorSubtitleTickDedupSkipsUnchangedWrite pins the CPU-burn
// guard: a tick whose built subtitle string is UNCHANGED must not
// re-write the view. Same-string ticks are reached with the fake clock
// by firing tickAll WITHOUT advancing the clock (the fake never
// auto-advances), so two consecutive ticks build the identical string —
// the harness equivalent of two ticks inside one frame bucket.
//
// The observable is the live view's cursor rather than a driver write
// counter: a changed string makes repaintQueryRunSubtitle re-run
// paintQueryEditorLeaf, whose FocusPoint overwrites (cx, cy) with the
// buffer cursor — so a cursor the test moved away is restored ONLY when
// the re-render happened. Deduped tick → cursor stays moved; changed
// tick → cursor snaps back (the positive control below).
func TestQueryEditorSubtitleTickDedupSkipsUnchangedWrite(t *testing.T) {
	g, drv, clk := newSubtitleGui(t)
	holdBusyForTest(t, g)

	g.SetQueryRunStarted("r1")
	tickAndWait(t, drv, clk, drv.statusWrites.Load(), 1)
	want1 := status.BuildRunningSubtitle(1, 100*time.Millisecond)
	if got := waitForSubtitle(t, drv, "first tick paint", func(s string) bool { return s == want1 }); got != want1 {
		t.Fatalf("subtitle = %q after first tick, want %q", got, want1)
	}

	// Move the cursor somewhere paintQueryEditorLeaf would never put it
	// (the empty buffer's cursor is (0,0)).
	drv.setViewCursorForTest(guicontext.QueryRailViewName, 9, 9)
	if cx, cy := drv.cursorOf(guicontext.QueryRailViewName); cx != 9 || cy != 9 {
		t.Fatalf("pre-condition: cursor = (%d,%d) after set, want (9,9)", cx, cy)
	}

	// Same-string tick: fire tickAll with NO clock advance (tickAndWait
	// would advance 100ms and change the built string), so the built
	// subtitle is identical to the last painted one. waitStatusWrites
	// proves the tick closure ran (its status repaint is unconditional);
	// acquiring the serialization mutex afterwards proves the subtitle
	// half of the same closure finished too.
	clk.tickAll()
	waitStatusWrites(t, drv, drv.statusWrites.Load(), 1)
	if cx, cy := drv.cursorOf(guicontext.QueryRailViewName); cx != 9 || cy != 9 {
		t.Fatalf("cursor = (%d,%d) after unchanged-string tick, want still (9,9) — the dedup guard re-rendered the view", cx, cy)
	}
	if got := drv.subtitleOf(guicontext.QueryRailViewName); got != want1 {
		t.Fatalf("subtitle = %q after unchanged-string tick, want unchanged %q", got, want1)
	}

	// Positive control: advance the clock so the built string changes —
	// the re-render MUST run and FocusPoint must restore the cursor to
	// the buffer cursor (0,0).
	clk.Advance(100 * time.Millisecond)
	clk.tickAll()
	want2 := status.BuildRunningSubtitle(2, 200*time.Millisecond)
	if got := waitForSubtitle(t, drv, "changed tick paint", func(s string) bool { return s == want2 }); got != want2 {
		t.Fatalf("subtitle = %q after changed tick, want %q", got, want2)
	}
	if cx, cy := drv.cursorOf(guicontext.QueryRailViewName); cx != 0 || cy != 0 {
		t.Fatalf("cursor = (%d,%d) after changed tick, want (0,0) — the re-render did not run FocusPoint", cx, cy)
	}
}
