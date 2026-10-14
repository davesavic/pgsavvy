package orchestrator_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
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
	for _, tc := range []struct {
		name                 string
		invalid, userBinding bool
	}{
		{"shipped keybinding information", false, false},
		{"user keybinding warning", false, true},
		{"config validation error", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.GetDefaultConfig()
			if tc.invalid {
				cfg.UI.ResultPageSize = -1
			}
			if tc.userBinding {
				cfg.Keybindings = append(cfg.Keybindings, config.KeybindingConfig{Mode: "n", Scope: "result_grid", Key: "y", Action: "result.yank.cell"})
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
			if tc.invalid {
				require.ErrorContains(t, err, "validation error")
				require.Contains(t, output.String(), "ui.result_page_size must be > 0")
				require.Contains(t, output.String(), `"level":"ERROR"`)
			} else {
				require.NoError(t, err)
				require.Contains(t, output.String(), "ambiguous_prefix")
				found := 0
				for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
					var record struct{ Level, Msg string }
					require.NoError(t, json.Unmarshal([]byte(line), &record))
					if !strings.Contains(record.Msg, "ambiguous_prefix") {
						continue
					}
					found++
					expectedLevel := "INFO"
					if tc.userBinding && strings.Contains(record.Msg, "scope=result_grid") {
						expectedLevel = "WARN"
					}
					require.Equal(t, expectedLevel, record.Level, record.Msg)
					require.Contains(t, record.Msg, "mode=normal")
					require.Contains(t, record.Msg, "action=")
					require.NotContains(t, record.Msg, "descendants: []")
					require.NotContains(t, record.Msg, "()")
				}
				require.Equal(t, 2, found)
			}
		})
	}
}
