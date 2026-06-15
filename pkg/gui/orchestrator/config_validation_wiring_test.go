package orchestrator_test

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/pgsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// TestWireWithDriver_ConfigValidation_RejectsInvalidConfig confirms the
// validation hook runs inside wireWithDriver — an invalid
// UserConfig (duplicate binding) causes UseDriverForTest to return an
// error, which RunAndHandleError would surface, and the deferred
// idempotent g.Close() at entry_point.go:133 unwinds cleanly.
func TestWireWithDriver_ConfigValidation_RejectsInvalidConfig(t *testing.T) {
	fs := afero.NewMemMapFs()
	cfg := config.GetDefaultConfig()
	// Duplicate binding on (mode=n, scope=global, key=Q) — both point at
	// real action ids that the cmdRegistry knows after wireWithDriver
	// populates it, so the only validation failure is the duplicate.
	cfg.Keybindings = append(cfg.Keybindings,
		config.KeybindingConfig{Mode: "n", Scope: "global", Key: "Q", Action: "app.quit"},
		config.KeybindingConfig{Mode: "n", Scope: "global", Key: "Q", Action: "help.cheatsheet"},
	)

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
	err := g.UseDriverForTest(rec)
	if err == nil {
		t.Fatalf("UseDriverForTest with duplicate binding: want error, got nil")
	}
	if !strings.Contains(err.Error(), "config:") {
		t.Fatalf("UseDriverForTest error: want 'config:' prefix, got %q", err.Error())
	}

	// AD-2 idempotent shutdown invariant: g.Close() is safe to call after
	// a failed wireWithDriver, mirroring entry_point.go's deferred close.
	if cerr := g.Close(); cerr != nil {
		t.Fatalf("Close after failed wire: want nil, got %v", cerr)
	}
	if cerr := g.Close(); cerr != nil {
		t.Fatalf("Close idempotent 2nd call: want nil, got %v", cerr)
	}
}

// TestWireWithDriver_ConfigValidation_AcceptsDefaultConfig is a sibling
// regression guard: the production default config must validate cleanly
// against the real cmdRegistry/ContextTree predicates.
func TestWireWithDriver_ConfigValidation_AcceptsDefaultConfig(t *testing.T) {
	g, _, _ := buildTestGuiWithCommon(t)
	if g.CommandRegistry() == nil {
		t.Fatalf("default config should produce a populated registry; wireWithDriver did not run")
	}
}
