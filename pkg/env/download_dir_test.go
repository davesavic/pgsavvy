package env

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGetDownloadDir_RespectsXDGEnv pins the highest-priority branch:
// XDG_DOWNLOAD_DIR wins over every other source when set.
func TestGetDownloadDir_RespectsXDGEnv(t *testing.T) {
	t.Setenv("XDG_DOWNLOAD_DIR", "/tmp/dbsavvy-xdg-download-test")
	if got := GetDownloadDir(); got != "/tmp/dbsavvy-xdg-download-test" {
		t.Fatalf("got %q, want '/tmp/dbsavvy-xdg-download-test'", got)
	}
}

// TestGetDownloadDir_FallsBackToHomeDownloads verifies that when
// XDG_DOWNLOAD_DIR is unset but $HOME/Downloads exists, that path is
// chosen ahead of os.TempDir().
func TestGetDownloadDir_FallsBackToHomeDownloads(t *testing.T) {
	t.Setenv("XDG_DOWNLOAD_DIR", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	downloads := filepath.Join(home, "Downloads")
	if err := os.Mkdir(downloads, 0o755); err != nil {
		t.Fatalf("mkdir Downloads: %v", err)
	}
	if got := GetDownloadDir(); got != downloads {
		t.Fatalf("got %q, want %q", got, downloads)
	}
}

// TestGetDownloadDir_FallsBackToTempDir verifies the last-ditch branch:
// XDG_DOWNLOAD_DIR unset and $HOME/Downloads missing returns
// os.TempDir().
func TestGetDownloadDir_FallsBackToTempDir(t *testing.T) {
	t.Setenv("XDG_DOWNLOAD_DIR", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Intentionally do NOT create $HOME/Downloads.
	if got := GetDownloadDir(); got != os.TempDir() {
		t.Fatalf("got %q, want os.TempDir() %q", got, os.TempDir())
	}
}
