package orchestrator_test

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/pgsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/models"
)

func TestStartupDiagnostics_UseConfiguredLogger(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		name := "keybinding warning"
		if invalid {
			name = "config validation error"
		}
		t.Run(name, func(t *testing.T) {
			cfg := config.GetDefaultConfig()
			if invalid {
				cfg.UI.ResultPageSize = -1
			}
			var output bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&output, nil))
			fs := afero.NewMemMapFs()
			c := common.NewCommon(logger, i18n.EnglishTranslationSet(), cfg, &common.AppState{}, fs)
			g := orchestrator.NewGui(orchestrator.Deps{
				Common:              c,
				Store:               common.NewAppStateStore(fs, "/state/state.yml", common.DefaultClock()),
				ConnectionsPath:     "/cfg/connections.yml",
				ConnectionsProvider: func() []models.Connection { return nil },
				DriverNamesFn:       func() []string { return []string{"postgres"} },
			})
			t.Cleanup(func() { _ = g.Close() })
			err := g.UseDriverForTest(testfake.NewRecorderGuiDriver())
			if invalid {
				require.ErrorContains(t, err, "validation error")
				require.Contains(t, output.String(), "ui.result_page_size must be > 0")
				require.Contains(t, output.String(), `"level":"ERROR"`)
			} else {
				require.NoError(t, err)
				require.Contains(t, output.String(), "ambiguous_prefix")
				require.Contains(t, output.String(), `"level":"WARN"`)
			}
		})
	}
}
