//go:build integration && linux

package integration_test

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/davesavic/pgsavvy/internal/machineio"
	"github.com/davesavic/pgsavvy/pkg/cli"
	"github.com/davesavic/pgsavvy/pkg/models"
	_ "modernc.org/sqlite"
)

func TestCLIHistoryLiveProcessHelper(t *testing.T) {
	if os.Getenv("PGSAVVY_HISTORY_LIVE_HELPER") != "1" {
		return
	}
	os.Exit(runLiveHistoryProcess())
}

func runLiveHistoryProcess() int {
	ctx, cancel := context.WithTimeout(context.Background(), cli.InvocationTimeout)
	defer cancel()
	out, err := machineio.OpenOutput(ctx, os.Stdout)
	if err != nil {
		return cli.ExitResource
	}
	defer func() { _ = out.Close() }()
	in, err := machineio.OpenInput(ctx, os.Stdin)
	if err != nil {
		return cli.ExitResource
	}
	defer func() { _ = in.Close() }()
	for i, arg := range os.Args {
		if arg == "--" {
			_, exit := cli.Dispatch(ctx, os.Args[i+1:], cli.IO{In: in, Out: out}, map[string]cli.Command{"query": cli.QueryCommand(cli.InvocationService{})})
			return exit
		}
	}
	return cli.ExitInput
}

func TestCLIHistory_Live(t *testing.T) {
	if os.Getenv(envDSN) == "" {
		t.Skip("PGSAVVY_TEST_PG is not configured; no live history verification performed")
	}
	// Reuse only the existing test-owned schema/role setup. Its default no-state
	// assertions stay intact: opted-in children use a separate disposable root.
	service, admin, schema := firstMachineFixture(t)
	profiles, err := service.Load()
	require.NoError(t, err)
	profile := profiles[0]
	profile.CLIHistory, profile.ReadOnly = true, false
	root := t.TempDir()
	configDir := filepath.Join(root, "config", "pgsavvy")
	require.NoError(t, os.MkdirAll(configDir, 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "home"), 0o700))
	configuration, err := yaml.Marshal(struct {
		Connections []models.Connection `yaml:"connections"`
	}{[]models.Connection{profile}})
	require.NoError(t, err)
	configPath := filepath.Join(configDir, "connections.yml")
	require.NoError(t, os.WriteFile(configPath, configuration, 0o600))
	childEnv := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(root, "home"),
		"XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "XDG_STATE_HOME=" + filepath.Join(root, "state"),
		"XDG_CACHE_HOME=" + filepath.Join(root, "cache"), "XDG_DATA_HOME=" + filepath.Join(root, "data"), "XDG_RUNTIME_DIR=" + filepath.Join(root, "runtime"),
		"PGPASSFILE=" + filepath.Join(root, "missing-pass"), "PGSERVICEFILE=" + filepath.Join(root, "missing-service"), "SSH_AUTH_SOCK=" + filepath.Join(root, "missing-agent"),
		"PGSAVVY_HISTORY_LIVE_HELPER=1",
	}
	table := pgx.Identifier{schema, "protected"}.Sanitize()
	cases := []struct {
		sql     string
		exit    int
		outcome string
	}{
		{"INSERT INTO " + table + " VALUES(2,'changed')", cli.ExitInput, "blocked"},
		{"WITH changed AS (DELETE FROM " + table + " RETURNING *) SELECT * FROM changed", cli.ExitInput, "blocked"},
		{"SELECT " + pgx.Identifier{schema, "attempt_write"}.Sanitize() + "()", cli.ExitDatabase, "failed"},
		{"SELECT count(*) FROM " + table, cli.ExitSuccess, "succeeded"},
	}
	for _, tc := range cases {
		file := filepath.Join(root, "query.sql")
		require.NoError(t, os.WriteFile(file, []byte(tc.sql), 0o600))
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCLIHistoryLiveProcessHelper$", "--", "query", "--connection=fixture", "--file="+file, "--timeout=5s")
		command.Env, command.Dir, command.Stdin = childEnv, root, bytes.NewReader(nil)
		output, runErr := command.Output()
		cancel()
		exit := 0
		if runErr != nil {
			var status *exec.ExitError
			require.ErrorAs(t, runErr, &status)
			exit = status.ExitCode()
		}
		require.Equal(t, tc.exit, exit, "live outcome changed")
		if tc.outcome == "failed" {
			require.Contains(t, string(output), `"sqlstate":"25006"`)
		}
		current, err := os.ReadFile(configPath)
		require.NoError(t, err)
		require.True(t, bytes.Equal(configuration, current), "recording changed fixture configuration")
	}
	var count int
	var value string
	require.NoError(t, admin.QueryRow(context.Background(), "SELECT count(*),min(value) FROM "+table).Scan(&count, &value))
	require.Equal(t, 1, count)
	require.Equal(t, "original", value)
	store := filepath.Join(root, "state", "pgsavvy", "history.sqlite")
	db, err := sql.Open("sqlite", "file:"+store+"?mode=ro")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT sql,source,connection_id,CASE WHEN blocked!=0 THEN 'blocked' WHEN succeeded!=0 THEN 'succeeded' ELSE 'failed' END FROM history ORDER BY id`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	i := 0
	for rows.Next() {
		require.Less(t, i, len(cases), "duplicate history event")
		var sql, source, connection, outcome string
		require.NoError(t, rows.Scan(&sql, &source, &connection, &outcome))
		require.Equal(t, cases[i].sql, sql)
		require.Equal(t, cases[i].outcome, outcome)
		require.Equal(t, "cli", source)
		require.Equal(t, "fixture", connection)
		i++
	}
	require.NoError(t, rows.Err())
	require.Equal(t, len(cases), i)
	for _, dir := range []string{"data", "cache", "runtime"} {
		_, err := os.Stat(filepath.Join(root, dir))
		require.ErrorIs(t, err, os.ErrNotExist)
	}
}
