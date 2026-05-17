package orchestrator_test

import (
	"testing"

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
	return g, rec
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
