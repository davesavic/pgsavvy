package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/spf13/afero"
)

func newBuildInfo() *BuildInfo {
	return &BuildInfo{Version: "test", Commit: "deadbeef", Date: "2026-05-21", BuildSource: "test"}
}

func TestWireSessionLogger_CreatesSessionFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	stateDir := "/state/dbsavvy"
	t.Setenv(disableSessionLogEnv, "")

	log, closer := wireSessionLogger(stateDir, fs, newBuildInfo())
	if log == nil {
		t.Fatal("wireSessionLogger returned nil logger")
	}
	if closer == nil {
		t.Fatal("wireSessionLogger returned nil closer; expected file-backed closer")
	}
	defer func() { _ = closer.Close() }()

	entries, err := afero.ReadDir(fs, filepath.Join(stateDir, "sessions"))
	if err != nil {
		t.Fatalf("ReadDir sessions: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 session file, got %d", len(entries))
	}
	name := entries[0].Name()
	if !strings.HasPrefix(name, "dbsavvy-") || !strings.HasSuffix(name, ".log") {
		t.Errorf("unexpected filename: %s", name)
	}
}

func TestWireSessionLogger_FallbackInstallsRedactionHook(t *testing.T) {
	// Empty stateDir → logs.Open returns an error → fallback path runs.
	t.Setenv(disableSessionLogEnv, "")
	fs := afero.NewMemMapFs()

	log, closer := wireSessionLogger("", fs, newBuildInfo())
	if log == nil {
		t.Fatal("fallback logger is nil")
	}
	if closer != nil {
		t.Fatal("fallback path must return nil closer")
	}
	if got := log.GetLevel(); got != logrus.WarnLevel {
		t.Errorf("fallback level = %v, want WarnLevel", got)
	}
	if total := countHooks(log); total == 0 {
		t.Errorf("fallback logger has 0 hooks; expected redactor installed (AD-13d)")
	}
}

func TestWireSessionLogger_RespectsDisableEnvVar(t *testing.T) {
	t.Setenv(disableSessionLogEnv, "1")
	fs := afero.NewMemMapFs()
	stateDir := "/state/dbsavvy"

	log, closer := wireSessionLogger(stateDir, fs, newBuildInfo())
	if log == nil {
		t.Fatal("disabled-path logger is nil")
	}
	if closer != nil {
		t.Fatal("disabled-path must return nil closer (no file opened)")
	}
	if got := log.GetLevel(); got != logrus.WarnLevel {
		t.Errorf("kill-switch level = %v, want WarnLevel", got)
	}
	if total := countHooks(log); total != 0 {
		t.Errorf("kill-switch logger has %d hooks; expected 0 (pre-feature parity)", total)
	}
	// No file should have been created.
	if exists, _ := afero.Exists(fs, filepath.Join(stateDir, "sessions")); exists {
		t.Error("kill-switch created sessions dir; expected no fs writes")
	}
}

func countHooks(l *logrus.Logger) int {
	n := 0
	for _, level := range logrus.AllLevels {
		n += len(l.Hooks[level])
	}
	return n
}
