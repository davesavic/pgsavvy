package app

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/dbsavvy/pkg/config"
)

func newBuildInfo() *BuildInfo {
	return &BuildInfo{Version: "test", Commit: "deadbeef", Date: "2026-05-21", BuildSource: "test"}
}

func TestWireSessionLogger_CreatesSessionFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	stateDir := "/state/dbsavvy"
	t.Setenv(disableSessionLogEnv, "")

	log, closer, err := wireSessionLogger(stateDir, false, fs, newBuildInfo())
	require.NoError(t, err)
	require.NotNil(t, log)
	require.NotNil(t, closer, "expected file-backed closer on primary path")
	defer func() { _ = closer.Close() }()

	entries, err := afero.ReadDir(fs, filepath.Join(stateDir, "sessions"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	name := entries[0].Name()
	require.True(t, strings.HasPrefix(name, "dbsavvy-") && strings.HasSuffix(name, ".log"),
		"unexpected filename: %s", name)
}

// TestWireSessionLogger_PrimaryPath_WritesJSONWithFourKeys validates AMD-F2-4:
// the primary file sink emits JSON lines with {level, msg, time, cat} keys.
func TestWireSessionLogger_PrimaryPath_WritesJSONWithFourKeys(t *testing.T) {
	fs := afero.NewMemMapFs()
	stateDir := "/state/dbsavvy"
	t.Setenv(disableSessionLogEnv, "")

	log, closer, err := wireSessionLogger(stateDir, false, fs, newBuildInfo())
	require.NoError(t, err)
	require.NotNil(t, log)
	require.NotNil(t, closer)

	log.LogAttrs(t.Context(), slog.LevelWarn, "smoke", slog.String("cat", "test"))
	require.NoError(t, closer.Close())

	entries, err := afero.ReadDir(fs, filepath.Join(stateDir, "sessions"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	f, err := fs.Open(filepath.Join(stateDir, "sessions", entries[0].Name()))
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	body, err := io.ReadAll(f)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	require.NotEmpty(t, lines)

	var rec map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &rec))
	for _, k := range []string{"level", "msg", "time", "cat"} {
		require.Contains(t, rec, k, "expected top-level key %q in JSON record: %v", k, rec)
	}
	require.Equal(t, "smoke", rec["msg"])
	require.Equal(t, "test", rec["cat"])
}

// TestWireSessionLogger_FallbackPath_RedactsDSN validates AMD-F2-4: when
// logs.Open fails (empty stateDir), the fallback logger still routes records
// through RedactingHandler so DSN credentials are scrubbed before stderr.
func TestWireSessionLogger_FallbackPath_RedactsDSN(t *testing.T) {
	t.Setenv(disableSessionLogEnv, "")

	got := withStderrCaptured(t, func() {
		// Empty stateDir → logs.Open returns an error → fallback path runs.
		log, closer, err := wireSessionLogger("", false, afero.NewMemMapFs(), newBuildInfo())
		require.NoError(t, err)
		require.NotNil(t, log)
		require.Nil(t, closer, "fallback path must return nil closer")
		log.Warn("connect failed", "dsn", "postgres://u:secret@h/d")
	})

	require.NotContains(t, got, "secret", "fallback path leaked plaintext password: %s", got)
	require.Contains(t, got, "***", "fallback path should mask the password with ***; got: %s", got)
}

// TestWireSessionLogger_DisableEnvVar validates AMD-F2-6: the kill-switch
// path emits only to stderr (no session file) while STILL applying the
// RedactingHandler so emergency-rollback users do not leak credentials.
func TestWireSessionLogger_DisableEnvVar(t *testing.T) {
	t.Setenv(disableSessionLogEnv, "1")
	fs := afero.NewMemMapFs()
	stateDir := "/state/dbsavvy"

	var log *slog.Logger
	var closer io.Closer
	got := withStderrCaptured(t, func() {
		var err error
		log, closer, err = wireSessionLogger(stateDir, false, fs, newBuildInfo())
		require.NoError(t, err)
		require.NotNil(t, log)
		require.Nil(t, closer, "kill-switch must return nil closer (no file opened)")
		log.Warn("rollback emit", "dsn", "postgres://u:hunter2@h/d")
	})

	// No file should have been created.
	exists, _ := afero.Exists(fs, filepath.Join(stateDir, "sessions"))
	require.False(t, exists, "kill-switch created sessions dir; expected no fs writes")

	require.NotContains(t, got, "hunter2", "kill-switch path leaked plaintext password: %s", got)
	require.Contains(t, got, "***", "kill-switch path should mask the password with ***; got: %s", got)
}

// TestResolveLogDir_Precedence covers dbsavvy-qbe: flag > env > stateDir, with
// empty/whitespace values falling through. The overridden bool MUST be true
// only when flag or env supplied the value (so mkdir failure on that path
// surfaces an error instead of falling back to stderr).
func TestResolveLogDir_Precedence(t *testing.T) {
	const stateDir = "/state/dbsavvy"

	tests := []struct {
		name      string
		flagVal   string
		envVal    string
		wantDir   string
		wantOverr bool
	}{
		{"flag wins over env", "/flag", "/env", "/flag", true},
		{"env used when no flag", "", "/env", "/env", true},
		{"state dir when neither set", "", "", stateDir, false},
		{"whitespace flag falls through to env", "   ", "/env", "/env", true},
		{"whitespace env falls through to state", "", "  \t ", stateDir, false},
		{"both whitespace falls through to state", " ", " ", stateDir, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, overr := resolveLogDir(tc.flagVal, tc.envVal, stateDir)
			require.Equal(t, tc.wantDir, got)
			require.Equal(t, tc.wantOverr, overr)
		})
	}
}

// TestWireSessionLogger_OverrideMkdirFailureSurfacesError validates the
// dbsavvy-qbe AC: "mkdir failure on override path returns clear error (not
// silent fallback)". When the operator explicitly chose a log dir via flag or
// env and logs.Open fails, the error MUST propagate so they see it.
func TestWireSessionLogger_OverrideMkdirFailureSurfacesError(t *testing.T) {
	t.Setenv(disableSessionLogEnv, "")
	// Empty Dir triggers logs.Open's "Options.Dir is empty" error path; we
	// don't actually need a real filesystem failure to exercise the branch.
	log, closer, err := wireSessionLogger("", true, afero.NewMemMapFs(), newBuildInfo())
	require.Error(t, err, "override path must surface logs.Open errors")
	require.Nil(t, log)
	require.Nil(t, closer)
}

// TestWireSessionLogger_OverrideDirIsHonored validates that when an explicit
// log dir is supplied, sessions land under <log-dir>/sessions/ (NOT under the
// process state dir). Pairs with state.yml staying at env.GetStateDir() —
// asserted indirectly by Start() not being exercised here.
func TestWireSessionLogger_OverrideDirIsHonored(t *testing.T) {
	fs := afero.NewMemMapFs()
	logDir := "/custom/logs"
	t.Setenv(disableSessionLogEnv, "")

	log, closer, err := wireSessionLogger(logDir, true, fs, newBuildInfo())
	require.NoError(t, err)
	require.NotNil(t, log)
	require.NotNil(t, closer)
	defer func() { _ = closer.Close() }()

	entries, err := afero.ReadDir(fs, filepath.Join(logDir, "sessions"))
	require.NoError(t, err)
	require.Len(t, entries, 1, "expected session file at override location")
}

// TestWireSessionLogger_DisableEnvWinsOverOverride validates that the
// DBSAVVY_DISABLE_SESSION_LOG kill switch still beats --log-dir /
// DBSAVVY_LOG_DIR. Operators must always be able to disable session logging
// regardless of which path was configured.
func TestWireSessionLogger_DisableEnvWinsOverOverride(t *testing.T) {
	t.Setenv(disableSessionLogEnv, "1")
	fs := afero.NewMemMapFs()

	log, closer, err := wireSessionLogger("/custom/logs", true, fs, newBuildInfo())
	require.NoError(t, err)
	require.NotNil(t, log)
	require.Nil(t, closer, "kill switch must return nil closer even with override set")

	exists, _ := afero.Exists(fs, "/custom/logs/sessions")
	require.False(t, exists, "kill switch must not create the override sessions dir")
}

// TestRequireQuitBinding covers dbsavvy-ivck.5 (T5, R5): the pre-NewGui
// guard returns a hard, actionable error naming app.quit and the config
// path when no quit binding is present, and returns nil otherwise. This is
// the seam Start() uses to abort BEFORE gocui.NewGui so the message
// survives tcell's Fini.
func TestRequireQuitBinding(t *testing.T) {
	const configPath = "/home/user/.config/dbsavvy/config.yml"

	t.Run("missing app.quit returns error naming action and path", func(t *testing.T) {
		cfg := config.GetDefaultConfig()
		cfg.Keybindings = []config.KeybindingConfig{
			{Mode: "n", Scope: "global", Key: "?", Action: "help.cheatsheet"},
		}
		err := requireQuitBinding(cfg, configPath)
		require.Error(t, err)
		require.Contains(t, err.Error(), config.QuitAction, "error must name the missing app.quit action")
		require.Contains(t, err.Error(), "app.quit", "error must literally contain app.quit")
		require.Contains(t, err.Error(), configPath, "error must point at the config file path")
		require.ErrorIs(t, err, config.ErrNoQuitBinding)
	})

	t.Run("config that binds app.quit returns nil", func(t *testing.T) {
		cfg := config.GetDefaultConfig() // defaults bind app.quit
		require.NoError(t, requireQuitBinding(cfg, configPath))

		cfg.Keybindings = []config.KeybindingConfig{
			{Mode: "n", Scope: "global", Key: "<c-c>", Action: "app.quit"},
		}
		require.NoError(t, requireQuitBinding(cfg, configPath))
	})
}

// withStderrCaptured redirects os.Stderr to a pipe for the duration of fn and
// returns whatever was written. Used to assert against fallback / kill-switch
// stderr output without coupling tests to slog handler internals.
func withStderrCaptured(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = orig })

	done := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- b
	}()

	fn()
	require.NoError(t, w.Close())
	captured := <-done
	return string(bytes.TrimSpace(captured))
}
