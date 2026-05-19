package orchestrator_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/sirupsen/logrus"
	"github.com/spf13/afero"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/gui/internal/testfake"
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
	return s.RecorderGuiDriver.FeedKey(view, key, mod)
}

// bootstrapAddConnGui wires a real *orchestrator.Gui against the recorder
// driver with an in-memory connections.yml. No real Postgres / sqlite is
// touched — HistoryProvider returns (nil, nil) so g.history stays nil and
// the query runtime never opens disk state.
func bootstrapAddConnGui(t *testing.T) (g *orchestrator.Gui, drv *serializedDriver, fs afero.Fs, connsPath string) {
	t.Helper()
	fs = afero.NewMemMapFs()
	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)
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

func typeString(t *testing.T, drv *serializedDriver, view, s string) {
	t.Helper()
	for _, r := range s {
		feedRune(t, drv, view, r)
	}
}

// TestConnectionAdd_HappyPath_AppendsOneRow drives the full three-step
// chained-prompt flow via RecorderGuiDriver.FeedKey and asserts the new
// profile lands in connections.yml verbatim.
func TestConnectionAdd_HappyPath_AppendsOneRow(t *testing.T) {
	g, rec, fs, path := bootstrapAddConnGui(t)

	pre, err := config.LoadConnections(fs, path)
	if err != nil {
		t.Fatalf("pre LoadConnections: %v", err)
	}
	if len(pre) != 0 {
		t.Fatalf("pre: got %d rows, want 0", len(pre))
	}

	// Step 0: trigger AddConnection via `a` on the CONNECTIONS rail.
	feedRune(t, rec, string(types.CONNECTIONS), 'a')

	// Step 1: SELECTION popup (driver picker).
	eventually(t, 2*time.Second, func() bool {
		ch := g.ChoiceHelperForTest()
		return ch != nil && ch.Active()
	}, "selection popup did not become active")
	// Cursor defaults to 0; with one driver ("postgres") <cr> picks it.
	feedSpecial(t, rec, string(types.SELECTION), gocui.KeyEnter)

	// Step 2: PROMPT popup (name).
	eventually(t, 2*time.Second, func() bool {
		ph := g.PromptHelperForTest()
		return ph != nil && ph.Active() && strings.Contains(ph.Label(), "Name")
	}, "prompt popup (Name) did not activate")
	typeString(t, rec, string(types.PROMPT), "alice")
	feedSpecial(t, rec, string(types.PROMPT), gocui.KeyEnter)

	// Step 3: PROMPT popup (DSN). The label is re-pushed by the adapter
	// so we must poll until it transitions from "Name" to "DSN".
	eventually(t, 2*time.Second, func() bool {
		ph := g.PromptHelperForTest()
		return ph != nil && ph.Active() && strings.Contains(ph.Label(), "DSN")
	}, "prompt popup (DSN) did not activate")
	typeString(t, rec, string(types.PROMPT), "postgres://localhost:5432/db")
	feedSpecial(t, rec, string(types.PROMPT), gocui.KeyEnter)

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
	g, rec, fs, path := bootstrapAddConnGui(t)

	feedRune(t, rec, string(types.CONNECTIONS), 'a')
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
