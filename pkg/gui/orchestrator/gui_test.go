package orchestrator_test

import (
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/afero"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/gui/editor"
	"github.com/davesavic/dbsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/dbsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// buildTestGui constructs a Gui with an in-memory fs and a recorder
// driver already installed (wireWithDriver run). Returns both for
// assertions.
func buildTestGui(t *testing.T) (*orchestrator.Gui, *testfake.RecorderGuiDriver) {
	t.Helper()
	g, rec, _ := buildTestGuiWithCommon(t)
	return g, rec
}

// buildTestGuiWithCommon is buildTestGui plus the *common.Common it built,
// for tests that need to assign field-only additions like LogCloser (AD-18).
func buildTestGuiWithCommon(t *testing.T) (*orchestrator.Gui, *testfake.RecorderGuiDriver, *common.Common) {
	return buildTestGuiWithLogger(t, slog.New(slog.DiscardHandler))
}

// buildTestGuiWithClock is buildTestGui with an injected Clock so the
// spinner-ticker tests (U8) can drive ticks and Now() deterministically.
func buildTestGuiWithClock(t *testing.T, clk orchestrator.Clock) (*orchestrator.Gui, *testfake.RecorderGuiDriver) {
	t.Helper()
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
	}, orchestrator.WithClock(clk))
	rec := testfake.NewRecorderGuiDriver()
	if err := g.UseDriverForTest(rec); err != nil {
		t.Fatalf("UseDriverForTest: %v", err)
	}
	return g, rec
}

// buildTestGuiWithLogger mirrors buildTestGuiWithCommon but injects the
// supplied *slog.Logger into the Common bag at construction time. Used
// by event-emission tests that need to capture cat=state lines.
func buildTestGuiWithLogger(t *testing.T, log *slog.Logger) (*orchestrator.Gui, *testfake.RecorderGuiDriver, *common.Common) {
	t.Helper()
	fs := afero.NewMemMapFs()
	cfg := config.GetDefaultConfig()
	c := common.NewCommon(log, i18n.EnglishTranslationSet(), cfg, &common.AppState{}, fs)
	store := common.NewAppStateStore(fs, "/tmp/state.yml", common.DefaultClock())

	g := orchestrator.NewGui(orchestrator.Deps{
		Common:              c,
		Store:               store,
		ConnectionsPath:     "/tmp/connections.yml",
		ConnectionsProvider: func() []models.Connection { return nil },
		DriverNamesFn:       func() []string { return []string{"postgres"} },
	})
	rec := testfake.NewRecorderGuiDriver()
	if err := g.UseDriverForTest(rec); err != nil {
		t.Fatalf("UseDriverForTest: %v", err)
	}
	return g, rec, c
}

// buildTestGuiWithDriverAndClock builds a Gui with a caller-supplied
// driver and Clock. Lets the spinner-ticker tests (U8) install a driver
// that observes UpdateContentOnly calls deterministically.
func buildTestGuiWithDriverAndClock(t *testing.T, drv types.GuiDriver, clk orchestrator.Clock) *orchestrator.Gui {
	t.Helper()
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
	}, orchestrator.WithClock(clk))
	if err := g.UseDriverForTest(drv); err != nil {
		t.Fatalf("UseDriverForTest: %v", err)
	}
	return g
}

func TestNewGuiAttachesControllers(t *testing.T) {
	g, _ := buildTestGui(t)
	if g.Controllers() == nil {
		t.Fatal("Controllers() is nil after wireWithDriver")
	}
	if g.Controllers().Schemas == nil {
		t.Fatal("SchemasController not attached")
	}
	if g.Controllers().Quit == nil {
		t.Fatal("QuitController not attached")
	}
	if g.Registry() == nil {
		t.Fatal("Registry() is nil after wireWithDriver")
	}
}

func TestNewGuiPushesConnectionManagerContextInitially(t *testing.T) {
	g, _ := buildTestGui(t)
	// startup pushes CONNECTION_MANAGER as root.
	// With a fresh AppStateStore + empty profiles
	// provider, the first-run tip is pushed on top.
	stack := g.ContextTree().Stack()
	if len(stack) < 2 {
		t.Fatalf("focus stack has %d entries after wireWithDriver, want >=2 (CONNECTION_MANAGER + FIRST_RUN_TIP)", len(stack))
	}
	if got := stack[0].GetKey(); got != types.CONNECTION_MANAGER {
		t.Fatalf("focus stack bottom = %q, want %q", got, types.CONNECTION_MANAGER)
	}
	if got := stack[len(stack)-1].GetKey(); got != types.FIRST_RUN_TIP {
		t.Fatalf("focus stack top = %q, want %q", got, types.FIRST_RUN_TIP)
	}
}

func TestRegisteredBindingsCoverEveryACKey(t *testing.T) {
	_, rec := buildTestGui(t)
	for _, expected := range testfake.ExpectedBindings {
		if !rec.HasKeybinding(expected.View, expected.Key, expected.Mod) {
			t.Errorf("missing binding view=%q key=%+v mod=%v", expected.View, expected.Key, expected.Mod)
		}
	}
}

func TestNewGuiHasChoiceHelperWired(t *testing.T) {
	g, _ := buildTestGui(t)
	if g.ChoiceHelperForTest() == nil {
		t.Fatal("choiceHelp is nil after wireWithDriver — ChainedPrompter adapter would be missing a picker")
	}
}

func TestNewGuiContextRegistryHasSelection(t *testing.T) {
	g, _ := buildTestGui(t)
	reg := g.Registry()
	if reg == nil {
		t.Fatal("Registry() is nil after wireWithDriver")
	}
	if reg.Selection == nil {
		t.Fatal("Registry().Selection is nil — SelectionContext not wired")
	}
}

// buildTestGuiWithAutocomplete builds a wired Gui whose
// editor.autocomplete flag is set to want.
func buildTestGuiWithAutocomplete(t *testing.T, want bool) *orchestrator.Gui {
	t.Helper()
	fs := afero.NewMemMapFs()
	cfg := config.GetDefaultConfig()
	cfg.Editor.Autocomplete = want
	c := common.NewCommon(slog.New(slog.DiscardHandler), i18n.EnglishTranslationSet(), cfg, &common.AppState{}, fs)
	store := common.NewAppStateStore(fs, "/tmp/state.yml", common.DefaultClock())

	g := orchestrator.NewGui(orchestrator.Deps{
		Common:              c,
		Store:               store,
		ConnectionsPath:     "/tmp/connections.yml",
		ConnectionsProvider: func() []models.Connection { return nil },
		DriverNamesFn:       func() []string { return []string{"postgres"} },
	})
	rec := testfake.NewRecorderGuiDriver()
	if err := g.UseDriverForTest(rec); err != nil {
		t.Fatalf("UseDriverForTest: %v", err)
	}
	return g
}

// queryEditorVimEditor extracts the QUERY_EDITOR VimEditor wired by
// installKeyDispatch, failing the test if it is missing or not a
// *editor.VimEditor.
func queryEditorVimEditor(t *testing.T, g *orchestrator.Gui) *editor.VimEditor {
	t.Helper()
	ed := g.MasterEditorForTest(types.QUERY_EDITOR)
	if ed == nil {
		t.Fatal("no master editor wired for QUERY_EDITOR")
	}
	ve, ok := ed.(*editor.VimEditor)
	if !ok {
		t.Fatalf("QUERY_EDITOR master editor is %T; want *editor.VimEditor", ed)
	}
	return ve
}

// TestAutoCompleterInstalledWhenFlagTrue pins the boot gate:
// with editor.autocomplete true (the default), the QUERY_EDITOR VimEditor
// has the as-you-type auto-completer wired.
func TestAutoCompleterInstalledWhenFlagTrue(t *testing.T) {
	g := buildTestGuiWithAutocomplete(t, true)
	if !queryEditorVimEditor(t, g).HasAutoCompleter() {
		t.Error("auto-completer not installed with editor.autocomplete=true")
	}
}

// TestAutoCompleterNotInstalledWhenFlagFalse pins that
// editor.autocomplete=false leaves the auto-completer uninstalled (no
// as-you-type popup). The manual <c-x><c-o> path is unaffected — it
// routes through the action registry / RefilterOrTrigger, not this seam
// (covered by the controller-level completion tests).
func TestAutoCompleterNotInstalledWhenFlagFalse(t *testing.T) {
	g := buildTestGuiWithAutocomplete(t, false)
	if queryEditorVimEditor(t, g).HasAutoCompleter() {
		t.Error("auto-completer installed with editor.autocomplete=false; want absent")
	}
}

func TestCloseIdempotent(t *testing.T) {
	g, _ := buildTestGui(t)
	if err := g.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Second Close must be a no-op (no panic, no error).
	if err := g.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// countingCloser records Close calls; satisfies io.Closer.
type countingCloser struct {
	calls atomic.Int32
	err   error
}

func (c *countingCloser) Close() error {
	c.calls.Add(1)
	return c.err
}

// slowCloser blocks Close until released or the test ends. Satisfies the
// pkg/logs.LogCloser interface so Gui.Close exercises the deadline path.
type slowCloser struct {
	release chan struct{}
	closed  atomic.Bool
}

func (s *slowCloser) Close() error {
	<-s.release
	s.closed.Store(true)
	return nil
}

func (s *slowCloser) CloseWithDeadline(d time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- s.Close() }()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		return errors.New("deadline exceeded")
	}
}

func TestClose_InvokesLogCloser_M15cStep7(t *testing.T) {
	g, _, cmn := buildTestGuiWithCommon(t)
	c := &countingCloser{}
	cmn.LogCloser = c

	if err := g.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := c.calls.Load(); got != 1 {
		t.Fatalf("LogCloser.Close call count = %d, want 1", got)
	}
}

func TestClose_LogCloserRespectsDeadline(t *testing.T) {
	g, _, cmn := buildTestGuiWithCommon(t)
	slow := &slowCloser{release: make(chan struct{})}
	cmn.LogCloser = slow
	t.Cleanup(func() { close(slow.release) })

	start := time.Now()
	_ = g.Close()
	elapsed := time.Since(start)
	// Deadline is 2 s; allow 1 s slack for scheduling noise.
	if elapsed > 3*time.Second {
		t.Fatalf("Close blocked for %v; expected ≤ 3 s under 2 s deadline", elapsed)
	}
	if elapsed < 1900*time.Millisecond {
		t.Fatalf("Close returned in %v; expected near 2 s deadline", elapsed)
	}
}

func TestQuitOnSignal_DoesNotCloseLoggerEarly(t *testing.T) {
	g, _, cmn := buildTestGuiWithCommon(t)
	c := &countingCloser{}
	cmn.LogCloser = c

	g.QuitOnSignal()
	// QuitOnSignal only enqueues a quit closure; it must never touch the
	// logger directly. Logger close is the responsibility of g.Close()
	// after MainLoop unwinds.
	if got := c.calls.Load(); got != 0 {
		t.Fatalf("QuitOnSignal invoked LogCloser %d times; expected 0", got)
	}
}
