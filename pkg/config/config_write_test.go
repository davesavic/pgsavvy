package config

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/spf13/afero"
)

func TestEnsureInitialConfig_CreatesWhenMissing(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/cfg/dbsavvy"
	if err := EnsureInitialConfig(fs, dir); err != nil {
		t.Fatalf("EnsureInitialConfig: %v", err)
	}
	path := filepath.Join(dir, "config.yml")
	info, err := fs.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.Mode().Perm() != os.FileMode(0o600) {
		t.Errorf("file mode = %v, want 0600", info.Mode().Perm())
	}
	dirInfo, err := fs.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	if dirInfo.Mode().Perm() != os.FileMode(0o700) {
		t.Errorf("dir mode = %v, want 0700", dirInfo.Mode().Perm())
	}
}

func TestEnsureInitialConfig_NoOpWhenPresent(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/cfg/dbsavvy"
	path := filepath.Join(dir, "config.yml")
	if err := fs.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const sentinel = "# user-edited content\nleader: q\n"
	if err := afero.WriteFile(fs, path, []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureInitialConfig(fs, dir); err != nil {
		t.Fatalf("EnsureInitialConfig: %v", err)
	}
	got, err := afero.ReadFile(fs, path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != sentinel {
		t.Errorf("file body changed; got %q, want %q", string(got), sentinel)
	}
	// Existing dir mode preserved (M10c).
	dirInfo, _ := fs.Stat(dir)
	if dirInfo.Mode().Perm() != os.FileMode(0o755) {
		t.Errorf("existing dir mode changed to %v; want 0755 preserved", dirInfo.Mode().Perm())
	}
}

func TestEnsureInitialConfig_TemplateHasNoCredentialFields(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/cfg/dbsavvy"
	if err := EnsureInitialConfig(fs, dir); err != nil {
		t.Fatalf("EnsureInitialConfig: %v", err)
	}
	body, err := afero.ReadFile(fs, filepath.Join(dir, "config.yml"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s := string(body)
	for _, banned := range []string{"password:", "password_command:", "dsn:"} {
		if strings.Contains(s, banned) {
			t.Errorf("template contains %q; want it absent\nbody: %s", banned, s)
		}
	}
}

func TestEnsureInitialConfig_ConcurrentSingleWinner(t *testing.T) {
	// Use os fs via temp dir so OpenFile O_EXCL semantics are exercised
	// realistically. MemMapFs honors O_EXCL too — we run both for coverage.
	t.Run("memfs", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		runConcurrent(t, fs, "/cfg/dbsavvy")
	})
	t.Run("osfs", func(t *testing.T) {
		dir := t.TempDir()
		fs := afero.NewOsFs()
		runConcurrent(t, fs, filepath.Join(dir, "dbsavvy"))
	})
}

func runConcurrent(t *testing.T, fs afero.Fs, dir string) {
	t.Helper()
	const N = 8
	var wg sync.WaitGroup
	var ok atomic.Int32
	wg.Add(N)
	for range N {
		go func() {
			defer wg.Done()
			if err := EnsureInitialConfig(fs, dir); err == nil {
				ok.Add(1)
			} else {
				t.Errorf("EnsureInitialConfig: %v", err)
			}
		}()
	}
	wg.Wait()
	// All callers report success (EEXIST is treated as success).
	if int(ok.Load()) != N {
		t.Errorf("ok = %d, want %d", ok.Load(), N)
	}
	// Exactly one file present.
	if exists, _ := afero.Exists(fs, filepath.Join(dir, "config.yml")); !exists {
		t.Errorf("config.yml not created")
	}
}
