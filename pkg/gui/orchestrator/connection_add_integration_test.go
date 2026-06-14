package orchestrator_test

import (
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/spf13/afero"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/query"
)

// serializedDriver wraps the recorder so every UI mutation (Update,
// UpdateContentOnly, FeedKey) takes a single mutex. Models the production
// gocui invariant — closures enqueued via driver.Update only run on the
// MainLoop goroutine. The recorder fires Update inline on whichever
// goroutine called it (typically a worker spawned by g.OnWorker), so
// without this serialization the worker goroutine's tree.Push (from a
// helper.Choose / helper.Prompt) races the test goroutine's tree.Pop
// (from a FeedKey-driven Cancel / Submit).
type serializedDriver struct {
	*testfake.RecorderGuiDriver
	mu sync.Mutex
}

func (s *serializedDriver) Update(fn func() error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RecorderGuiDriver.Update(fn)
}

func (s *serializedDriver) UpdateContentOnly(fn func() error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RecorderGuiDriver.UpdateContentOnly(fn)
}

// FeedKeySerialized dispatches the keystroke under the UI mutex so the
// handler chain it kicks off (matcher.Dispatch → controller → helper →
// tree.Pop) cannot interleave with an OnUIThread closure running on the
// worker goroutine.
func (s *serializedDriver) FeedKeySerialized(view string, key types.Key, mod types.Modifier) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.FeedKey(view, key, mod)
}

// assertPopupRendered drives RunLayout (mirroring the production
// MainLoop) and asserts the named popup view was SetView'd this pass.
//
// Regression guard for the SELECTION popup vanishing because
// layout.popupRectFor lacked a case branch for it: ChoiceHelper.Active()
// returned true (the helper bookkeeping is correct), but the Tier-3
// layout pass skipped SetView for "selection" and the user saw a focus
// loss with no visible popup. Any future popup-context that ships a
// helper-Active flag without a popupRectFor entry should fail this
// check.
//
// RunLayout is called WITHOUT the serializedDriver mutex: HandleRender
// fan-outs invoke driver.Update reentrantly, which would deadlock under
// the non-reentrant mutex. Callers must invoke this only when the worker
// goroutine is parked on its result channel (i.e. immediately after
// eventually(Active) returns), so no concurrent driver.Update can race
// the layout pass's stack walk.
func assertPopupRendered(t *testing.T, g *orchestrator.Gui, rec *serializedDriver, viewName string) {
	t.Helper()
	if err := g.RunLayout(80, 24); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	if !rec.HasSetView(viewName) {
		t.Fatalf("popup %q helper reports Active() but RunLayout did not SetView the popup; "+
			"likely missing popupRectFor case branch in pkg/gui/orchestrator/layout.go", viewName)
	}
}

// assertPopupBodyContains drives RunLayout and asserts the named popup
// view's buffer contains every substring in wants. Guards against the
// SELECTION/PROMPT bodies being empty boxes — both contexts only paint
// content when SetState has been wired to the live helper state.
func assertPopupBodyContains(t *testing.T, g *orchestrator.Gui, rec *serializedDriver, viewName string, wants ...string) {
	t.Helper()
	if err := g.RunLayout(80, 24); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	body := rec.GetViewBuffer(viewName)
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Fatalf("popup %q body missing %q; body=%q", viewName, w, body)
		}
	}
}

// assertPromptCursorAt drives a layout pass and asserts the most-recent
// SetViewCursor call for the PROMPT view lands at (wantX, wantY). The
// PROMPT body is "<label>\n\n> <buffer>", so the caret belongs at the
// end of the buffer on line 2: y=2, x=2+len(buffer). Mirrors the
// COMMAND_LINE caret-anchoring guard.
func assertPromptCursorAt(t *testing.T, g *orchestrator.Gui, rec *serializedDriver, wantX, wantY int) {
	t.Helper()
	if err := g.RunLayout(80, 24); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	calls := rec.AllSetViewCursorCalls()
	for _, c := range slices.Backward(calls) {

		if c.View != string(types.PROMPT) {
			continue
		}
		if c.X != wantX || c.Y != wantY {
			t.Fatalf("SetViewCursor(%q) = (%d, %d), want (%d, %d)", c.View, c.X, c.Y, wantX, wantY)
		}
		return
	}
	t.Fatalf("no SetViewCursor call recorded for view %q; all calls=%+v", string(types.PROMPT), calls)
}

// assertCaretEnabled asserts the global gocui caret is on. SetViewCursor
// positions the caret, but gocui's flush only renders the cursor when
// g.Cursor (toggled via SetCaretEnabled) is true. Without this guard the
// PROMPT popup writes its cursor position but no caret appears in the
// real TUI.
func assertCaretEnabled(t *testing.T, rec *serializedDriver, want bool) {
	t.Helper()
	if got := rec.CaretEnabled; got != want {
		t.Fatalf("CaretEnabled = %v, want %v (log=%v)", got, want, rec.AllCaretEnabledLog())
	}
}

// bootstrapAddConnGui wires a real *orchestrator.Gui against the recorder
// driver with an in-memory connections.yml. No real Postgres / sqlite is
// touched — HistoryProvider returns (nil, nil) so g.history stays nil and
// the query runtime never opens disk state.
func bootstrapAddConnGui(t *testing.T) (g *orchestrator.Gui, drv *serializedDriver, fs afero.Fs, connsPath string) {
	t.Helper()
	fs = afero.NewMemMapFs()
	log := slog.New(slog.DiscardHandler)
	cfg := config.GetDefaultConfig()
	tr := i18n.EnglishTranslationSet()
	c := common.NewCommon(log, tr, cfg, &common.AppState{}, fs)
	store := common.NewAppStateStore(fs, "/state/state.yml", common.DefaultClock())
	connsPath = "/cfg/connections.yml"

	g = orchestrator.NewGui(orchestrator.Deps{
		Common:              c,
		Store:               store,
		ConnectionsPath:     connsPath,
		ConnectionsProvider: func() []models.Connection { return nil },
		DriverNamesFn:       func() []string { return []string{"postgres"} },
		HistoryProvider:     func() (*query.History, error) { return nil, nil },
	})
	drv = &serializedDriver{RecorderGuiDriver: testfake.NewRecorderGuiDriver()}
	if err := g.UseDriverForTest(drv); err != nil {
		t.Fatalf("UseDriverForTest: %v", err)
	}
	drv.SetManager(g)
	t.Cleanup(func() { _ = g.Close() })
	return g, drv, fs, connsPath
}

// eventually polls pred at a small interval until it returns true or
// timeout elapses. Fails the test with msg on timeout. No fixed sleeps
// exceed 10ms — required by the AC's "no fixed sleeps >50ms" rule.
func eventually(t *testing.T, timeout time.Duration, pred func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !pred() {
		t.Fatalf("eventually: %s (timeout %s)", msg, timeout)
	}
}

func feedRune(t *testing.T, drv *serializedDriver, view string, r rune) {
	t.Helper()
	if err := drv.FeedKeySerialized(view, gocui.NewKeyRune(r), types.ModNone); err != nil {
		t.Fatalf("FeedKey(view=%q, rune=%q): %v", view, r, err)
	}
}

func feedSpecial(t *testing.T, drv *serializedDriver, view string, name gocui.KeyName) {
	t.Helper()
	if err := drv.FeedKeySerialized(view, gocui.NewKeyName(name), types.ModNone); err != nil {
		t.Fatalf("FeedKey(view=%q, special=%v): %v", view, name, err)
	}
}

// feedSpecialEditable dispatches a special key through the recorder's
// master Editor instead of the SetKeybinding shim list. Editable views
// (that now includes PROMPT) install a master Editor
// and SKIP per-key SetKeybinding shims, so FeedKey for Enter/Esc would
// return errNotFound — FeedChord drives the same Dispatcher path the
// production gocui Editor uses, so the matcher resolves Enter →
// PromptSubmit (or Esc → PromptCancel) and the controller's handler
// fires.
func feedSpecialEditable(t *testing.T, drv *serializedDriver, view string, name gocui.KeyName) {
	t.Helper()
	drv.mu.Lock()
	defer drv.mu.Unlock()
	chordKey := keys.KeyFromGocui(gocui.NewKeyName(name))
	if _, err := drv.FeedChord(view, []keys.Key{chordKey}); err != nil {
		t.Fatalf("FeedChord(view=%q, special=%v): %v", view, name, err)
	}
}

// TestConnectionAdd_HappyPath_AppendsOneRow drives the full three-step
// chained-prompt flow via RecorderGuiDriver.FeedKey and asserts the new
// profile lands in connections.yml verbatim.
func TestConnectionAdd_HappyPath_AppendsOneRow(t *testing.T) {
	t.Skip("CONNECTIONS rail removed; WalkAdd chained prompt flow retired in favor of inline modal form")
	g, rec, fs, path := bootstrapAddConnGui(t)

	pre, err := config.LoadConnections(fs, path)
	if err != nil {
		t.Fatalf("pre LoadConnections: %v", err)
	}
	if len(pre) != 0 {
		t.Fatalf("pre: got %d rows, want 0", len(pre))
	}

	// Step 0: trigger AddConnection via `a` on the CONNECTIONS rail.
	feedRune(t, rec, string(types.CONNECTION_MANAGER), 'a')

	// Step 1: SELECTION popup (driver picker).
	eventually(t, 2*time.Second, func() bool {
		ch := g.ChoiceHelperForTest()
		return ch != nil && ch.Active()
	}, "selection popup did not become active")
	assertPopupRendered(t, g, rec, string(types.SELECTION))
	assertPopupBodyContains(t, g, rec, string(types.SELECTION), "Pick a driver", "postgres")
	// Cursor defaults to 0; with one driver ("postgres") <cr> picks it.
	feedSpecial(t, rec, string(types.SELECTION), gocui.KeyEnter)

	// Step 2: PROMPT popup (name).
	eventually(t, 2*time.Second, func() bool {
		ph := g.PromptHelperForTest()
		return ph != nil && ph.Active() && strings.Contains(ph.Label(), "Name")
	}, "prompt popup (Name) did not activate")
	assertPopupRendered(t, g, rec, string(types.PROMPT))
	assertPopupBodyContains(t, g, rec, string(types.PROMPT), "Connection name")
	assertCaretEnabled(t, rec, true)
	// PROMPT is editable: in production gocui.DefaultEditor
	// writes keystrokes into v.TextArea, but the recorder driver returns
	// nil from SetView so the TextArea path is unreachable. Inject the
	// typed value via the test seam so PromptController.Submit reads
	// "alice" from the context's test-mode buffer when <cr> fires.
	g.SeedPromptBufferForTest("alice")
	// Caret anchor: with buffer="alice" the layout pass should call
	// SetViewCursor("prompt", 2+len("alice"), 2). The "> " prefix is two
	// cells on body line 2 (label=0, blank=1, "> <buf>"=2). Without this
	// the user sees the typed text but no caret showing where their next
	// character will land (PROMPT caret bug).
	assertPromptCursorAt(t, g, rec, 2+len("alice"), 2)
	feedSpecialEditable(t, rec, string(types.PROMPT), gocui.KeyEnter)

	// Step 3: PROMPT popup (DSN). The label is re-pushed by the adapter
	// so we must poll until it transitions from "Name" to "DSN".
	eventually(t, 2*time.Second, func() bool {
		ph := g.PromptHelperForTest()
		return ph != nil && ph.Active() && strings.Contains(ph.Label(), "DSN")
	}, "prompt popup (DSN) did not activate")
	g.SeedPromptBufferForTest("postgres://localhost:5432/db")
	feedSpecialEditable(t, rec, string(types.PROMPT), gocui.KeyEnter)

	// Worker quiesces when WalkAddConnection returns. Poll on the
	// filesystem rather than g.BusyCount so we test the user-visible
	// artifact end-to-end.
	eventually(t, 2*time.Second, func() bool {
		loaded, err := config.LoadConnections(fs, path)
		return err == nil && len(loaded) == 1
	}, "connection was not appended within 2s")

	loaded, err := config.LoadConnections(fs, path)
	if err != nil {
		t.Fatalf("post LoadConnections: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("post: got %d rows, want 1", len(loaded))
	}
	got := loaded[0]
	want := models.Connection{Name: "alice", Driver: "postgres", DSN: "postgres://localhost:5432/db"}
	if got.Name != want.Name || got.Driver != want.Driver || got.DSN != want.DSN {
		t.Fatalf("loaded[0] = %+v, want %+v", got, want)
	}

	// Both popups must be inert after success — no leaks across tests.
	eventually(t, 1*time.Second, func() bool {
		ch := g.ChoiceHelperForTest()
		ph := g.PromptHelperForTest()
		return ch != nil && !ch.Active() && ph != nil && !ph.Active()
	}, "popups still active after happy path")

	// No update closures returned an error.
	if errs := rec.UpdateErrors(); len(errs) != 0 {
		t.Fatalf("Update closure errors: %v", errs)
	}
}

// TestConnectionAdd_EscAtDriverPick_NoWrite asserts that pressing <esc>
// at the SELECTION popup unwinds the entire WalkAddConnection sequence
// cleanly with no file write and no leaked popup state.
func TestConnectionAdd_EscAtDriverPick_NoWrite(t *testing.T) {
	t.Skip("CONNECTIONS rail removed; WalkAdd chained prompt flow retired in favor of inline modal form")
	g, rec, fs, path := bootstrapAddConnGui(t)

	feedRune(t, rec, string(types.CONNECTION_MANAGER), 'a')
	eventually(t, 2*time.Second, func() bool {
		ch := g.ChoiceHelperForTest()
		return ch != nil && ch.Active()
	}, "selection popup did not become active")

	feedSpecial(t, rec, string(types.SELECTION), gocui.KeyEsc)

	// Popup closes; worker returns nil (translateCancel collapses
	// PromptCanceledErr to nil); WaitWorkers gives a deterministic
	// quiescence point.
	eventually(t, 2*time.Second, func() bool {
		ch := g.ChoiceHelperForTest()
		return ch != nil && !ch.Active()
	}, "selection popup did not close after <esc>")
	g.WaitWorkers()

	loaded, err := config.LoadConnections(fs, path)
	if err != nil {
		t.Fatalf("LoadConnections: %v", err)
	}
	if len(loaded) != 0 {
		t.Fatalf("after esc: got %d rows, want 0", len(loaded))
	}

	ph := g.PromptHelperForTest()
	if ph != nil && ph.Active() {
		t.Fatal("prompt helper active after cancel")
	}

	if errs := rec.UpdateErrors(); len(errs) != 0 {
		t.Fatalf("Update closure errors: %v", errs)
	}
}
