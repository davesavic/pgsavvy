package orchestrator_test

import (
	"fmt"
	"strings"
	"testing"
	"unicode"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

func TestCLIHistoryDisclosure_RealWrappedFormNavigation(t *testing.T) {
	for _, size := range [][2]int{{80, 10}, {80, 12}, {80, 24}, {40, 10}, {20, 12}, {10, 10}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			fs := newConnectionsTestFs()
			const path = "/cfg/connections.yml"
			require.NoError(t, config.SaveConnections(fs, path, []models.Connection{{Name: "dev", Driver: "postgres"}}))
			before, err := afero.ReadFile(fs, path)
			require.NoError(t, err)
			g, rec := bootstrapSaveGui(t, fs, path)
			rec.EnableRealView(string(types.CONNECTION_MANAGER))
			modal := pushModal(t, g)
			ctrl := g.Controllers().ConnectionManager
			require.NoError(t, ctrl.Edit(commands.ExecCtx{}))
			renderMarker := func() string {
				t.Helper()
				require.NoError(t, g.RunLayout(size[0], size[1]))
				v := rec.RealView(string(types.CONNECTION_MANAGER))
				require.NotNil(t, v)
				// The recorder stores content separately; feed it into the real
				// gocui wrapper before exercising production scrolling.
				v.SetContent(rec.GetViewBuffer(string(types.CONNECTION_MANAGER)))
				require.NoError(t, g.RunLayout(size[0], size[1]))
				lines := v.ViewBufferLines()
				start := v.OriginY()
				for _, line := range lines[start:min(len(lines), start+v.InnerHeight())] {
					if strings.HasPrefix(line, "> ") || line == ">" {
						return strings.TrimPrefix(line, "> ")
					}
				}
				t.Fatalf("focused form row %q is not visible: origin=%d height=%d lines=%q", modal.FormFocusedLabel(), start, v.InnerHeight(), lines[start:min(len(lines), start+v.InnerHeight())])
				return ""
			}
			checkDisclosure := func(text string) {
				t.Helper()
				text = strings.Map(func(r rune) rune {
					if unicode.IsSpace(r) {
						return -1
					}
					return r
				}, text)
				for _, warning := range []string{"RawSQL(literals/comments)issaved.", "Noboundparameters/results.Notanauditlog.", "Permissionremediation:stophistorywriters;", "inspect$XDG_STATE_HOME/pgsavvy/history.sqlite,", "itsdirectoryandWAL/SHM/journalsidecars.", "Restrictaccesstoowner;noautomaticrepair."} {
					require.Contains(t, text, warning)
				}
			}
			var downDisclosure, upDisclosure []string
			fields := map[string]bool{}
			for i := 0; i < 2000; i++ {
				label := modal.FormFocusedLabel()
				fields[label] = true
				marker := renderMarker()
				if label == "cli_history" {
					downDisclosure = append(downDisclosure, marker)
				}
				if label == "password_command" {
					break
				}
				require.NoError(t, ctrl.Down(commands.ExecCtx{}))
			}
			require.Equal(t, "password_command", modal.FormFocusedLabel())
			for i := 0; i < 2000; i++ {
				label := modal.FormFocusedLabel()
				delete(fields, label)
				marker := renderMarker()
				if label == "cli_history" {
					upDisclosure = append([]string{marker}, upDisclosure...)
				}
				if label == "name" {
					break
				}
				require.NoError(t, ctrl.Up(commands.ExecCtx{}))
			}
			require.Equal(t, "name", modal.FormFocusedLabel())
			require.Empty(t, fields, "every field remains accessible in both directions")
			checkDisclosure(strings.Join(downDisclosure, ""))
			checkDisclosure(strings.Join(upDisclosure, ""))
			after, err := afero.ReadFile(fs, path)
			require.NoError(t, err)
			require.Equal(t, before, after, "navigation must not save consent")
			focusConnectionFormField(t, modal, "cli_history")
			if size[1] == 12 {
				require.NoError(t, ctrl.Edit(commands.ExecCtx{}))
			} else {
				require.NoError(t, ctrl.Toggle(commands.ExecCtx{}))
			}
			renderMarker()
			if size[1] == 10 {
				focusConnectionFormField(t, modal, "name")
				renderMarker()
			}
			require.NoError(t, ctrl.Confirm(commands.ExecCtx{}))
			loaded, err := config.LoadConnections(fs, path)
			require.NoError(t, err)
			require.True(t, loaded[0].CLIHistory)
		})
	}
}
