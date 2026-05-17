package logs

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/adrg/xdg"
)

// withXDGStateHome sets XDG_STATE_HOME for the test, reloads xdg's cached
// paths, and restores them after the test via t.Cleanup.
func withXDGStateHome(t *testing.T, path string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", path)
	xdg.Reload()
	t.Cleanup(func() { xdg.Reload() })
}

func TestInit_CreatesLogFileWithMode0600(t *testing.T) {
	tmp := t.TempDir()
	withXDGStateHome(t, tmp)

	logger, err := Init()
	if err != nil {
		t.Fatalf("Init returned err: %v", err)
	}
	if logger == nil {
		t.Fatal("Init returned nil logger")
	}

	parent := filepath.Join(tmp, "dbsavvy")
	logPath := filepath.Join(parent, "dbsavvy.log")

	parentInfo, err := os.Stat(parent)
	if err != nil {
		t.Fatalf("stat parent: %v", err)
	}
	if !parentInfo.IsDir() {
		t.Fatalf("parent %q is not a dir", parent)
	}
	if got := parentInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("parent perm = %o, want 0700", got)
	}

	logInfo, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat log file: %v", err)
	}
	if got := logInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("log file perm = %o, want 0600", got)
	}
}

func TestInit_AppendsToExistingFile(t *testing.T) {
	tmp := t.TempDir()
	withXDGStateHome(t, tmp)

	logger1, err := Init()
	if err != nil {
		t.Fatalf("first Init: %v", err)
	}
	logger1.Info("hello-1")

	logger2, err := Init()
	if err != nil {
		t.Fatalf("second Init: %v", err)
	}
	logger2.Info("hello-2")

	content, err := os.ReadFile(filepath.Join(tmp, "dbsavvy", "dbsavvy.log"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(content), "hello-1") || !strings.Contains(string(content), "hello-2") {
		t.Fatalf("expected both lines in log, got: %s", content)
	}
}

func TestInit_ReturnsErrorWhenHomeAndXDGStateHomeUnset(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("HOME/XDG_STATE_HOME unset behavior is platform-specific; verified on linux")
	}
	t.Setenv("HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	xdg.Reload()
	t.Cleanup(func() { xdg.Reload() })

	logger, err := Init()
	if err == nil {
		t.Fatal("expected non-nil error when HOME and XDG_STATE_HOME both unset")
	}
	if logger != nil {
		t.Fatal("expected nil logger on error")
	}
	if !strings.Contains(err.Error(), "state dir") && !strings.Contains(err.Error(), "HOME") {
		t.Fatalf("error %q lacks recognizable message", err.Error())
	}
}

func TestInit_ReturnsErrorWhenParentReadOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses read-only dir perms")
	}
	tmp := t.TempDir()
	readonly := filepath.Join(tmp, "ro")
	if err := os.Mkdir(readonly, 0o500); err != nil {
		t.Fatalf("mkdir readonly: %v", err)
	}
	// Make our target stateDir a child the test cannot create under ro/.
	withXDGStateHome(t, readonly)

	_, err := Init()
	if err == nil {
		t.Fatal("expected error when parent dir is read-only")
	}
}
