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
)

func newBuildInfo() *BuildInfo {
	return &BuildInfo{Version: "test", Commit: "deadbeef", Date: "2026-05-21", BuildSource: "test"}
}

func TestWireSessionLogger_CreatesSessionFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	stateDir := "/state/dbsavvy"
	t.Setenv(disableSessionLogEnv, "")

	log, closer := wireSessionLogger(stateDir, fs, newBuildInfo())
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

	log, closer := wireSessionLogger(stateDir, fs, newBuildInfo())
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
		log, closer := wireSessionLogger("", afero.NewMemMapFs(), newBuildInfo())
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
		log, closer = wireSessionLogger(stateDir, fs, newBuildInfo())
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
