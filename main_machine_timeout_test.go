//go:build linux || darwin

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestMachineCLINative_QueryTimeout(t *testing.T) {
	dsn := os.Getenv("PGSAVVY_TEST_PG")
	if dsn == "" {
		t.Skip("requires designated disposable PGSAVVY_TEST_PG; not live evidence")
	}
	for _, mode := range []string{"row-read", "full-read-only", "full"} {
		for _, tc := range []struct {
			name, timeout, profile, sql string
			exit                        int
		}{
			{"extended", "2m", "", "SELECT pg_sleep(11)", 0},
			{"default", "", "", "SELECT pg_sleep(11)", 6},
			{"short", "500ms", "", "SELECT pg_sleep(11)", 6},
			{"saved-shorter", "2m", "200ms", "SELECT pg_sleep(11)", 6},
			{"over-30s", "2m", "", "SELECT pg_sleep(31)", 0},
			{"maximum", "2147483647ms", "", "SELECT 1", 0},
			{"parameterized", "2m", "", "SELECT pg_sleep($1::double precision)", 0},
			{"batch", "2m", "", "SELECT pg_sleep(11); SELECT 1", 0},
		} {
			if tc.name == "batch" && mode != "full" {
				continue
			}
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				root := t.TempDir()
				profile := models.Connection{Name: "timeout-test", Driver: "postgres", DSN: dsn, CLIAccess: models.CLIAccessRowRead, StatementTimeout: tc.profile}
				if mode != "row-read" {
					profile.CLIAccess = models.CLIAccessFull
					profile.ReadOnly = mode == "full-read-only"
				}
				data, err := yaml.Marshal(struct {
					Connections []models.Connection `yaml:"connections"`
				}{[]models.Connection{profile}})
				require.NoError(t, err)
				dir := filepath.Join(root, "config", "pgsavvy")
				require.NoError(t, os.MkdirAll(dir, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "connections.yml"), data, 0o600))
				args := []string{"query", "--connection=timeout-test", "--file=-"}
				if tc.name == "parameterized" {
					path := filepath.Join(root, "params.json")
					require.NoError(t, os.WriteFile(path, []byte("[11]"), 0o600))
					args = append(args, "--params-file="+path)
				}
				if tc.timeout != "" {
					args = append(args, "--timeout="+tc.timeout)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, machineTestBinary, args...)
				command.Env = machineTestEnv(root)
				command.Stdin = strings.NewReader(tc.sql)
				var out, diagnostic bytes.Buffer
				command.Stdout, command.Stderr = &out, &diagnostic
				err = command.Run()
				if tc.exit == 0 {
					require.NoError(t, err, out.String()+diagnostic.String())
				} else {
					var exit *exec.ExitError
					require.ErrorAs(t, err, &exit)
					require.Equal(t, tc.exit, exit.ExitCode(), out.String()+diagnostic.String())
				}
				doc := machineTestDocument(t, out.Bytes(), tc.exit)
				if mode == "full" && tc.exit == 0 {
					require.Equal(t, "completed", doc["execution"].(map[string]any)["status"])
				}
				if tc.exit != 0 {
					require.Equal(t, "TIMEOUT", doc["error"].(map[string]any)["code"])
				}
				if tc.name == "default" || tc.name == "saved-shorter" {
					require.Equal(t, "57014", doc["error"].(map[string]any)["sqlstate"])
				}
			})
		}
	}
}
