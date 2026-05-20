package orchestrator_test

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/afero"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/config"
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
	t.Helper()
	fs := afero.NewMemMapFs()
	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)
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

func TestNewGuiAttachesControllers(t *testing.T) {
	g, _ := buildTestGui(t)
	if g.Controllers() == nil {
		t.Fatal("Controllers() is nil after wireWithDriver")
	}
	if g.Controllers().Connections == nil {
		t.Fatal("ConnectionsController not attached")
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

func TestNewGuiPushesConnectionsContextInitially(t *testing.T) {
	g, _ := buildTestGui(t)
	top := g.ContextTree().Current()
	if top == nil {
		t.Fatal("focus stack is empty after wireWithDriver")
	}
	if got := top.GetKey(); got != types.CONNECTIONS {
		t.Fatalf("initial context = %q, want %q", got, types.CONNECTIONS)
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
		t.Fatal("Registry().Selection is nil — m47.2 SelectionContext not wired")
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
