package logs

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/afero"
)

// fakeClock advances on demand; Now() returns the current value.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

// readSessionFiles returns the names of *.log session files (sorted).
func readSessionFiles(t *testing.T, fs afero.Fs, dir string) []string {
	t.Helper()
	entries, err := afero.ReadDir(fs, dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), sessionFilePrefix) && strings.HasSuffix(e.Name(), sessionFileSuffix) {
			names = append(names, e.Name())
		}
	}
	return names
}

func TestOpen_CreatesSessionFileWithPerms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix perms")
	}
	tmp := t.TempDir()
	l, closer, err := Open(Options{Dir: tmp})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = closer.Close() }()
	if l == nil {
		t.Fatal("nil logger")
	}

	sessionsDir := filepath.Join(tmp, sessionsSubdir)
	names := readSessionFiles(t, afero.NewOsFs(), sessionsDir)
	if len(names) != 1 {
		t.Fatalf("expected 1 session file, got %v", names)
	}
	info, err := os.Stat(filepath.Join(sessionsDir, names[0]))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("perm = %o, want 0600", got)
	}
}

func TestOpen_NanosecondSuffixUnique(t *testing.T) {
	fs := afero.NewMemMapFs()
	clk := &fakeClock{now: time.Date(2026, 5, 21, 12, 0, 0, 100, time.UTC)}

	_, c1, err := Open(Options{Dir: "/state", FS: fs, Clock: clk, Pid: 4242})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	defer func() { _ = c1.Close() }()

	clk.now = clk.now.Add(1 * time.Nanosecond)

	_, c2, err := Open(Options{Dir: "/state", FS: fs, Clock: clk, Pid: 4242})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer func() { _ = c2.Close() }()

	names := readSessionFiles(t, fs, "/state/"+sessionsSubdir)
	if len(names) != 2 {
		t.Fatalf("expected 2 session files, got %v", names)
	}
	if names[0] == names[1] {
		t.Fatalf("expected distinct names, got duplicates: %v", names)
	}
}

func TestOpen_PrunesOldestBeyondRetention(t *testing.T) {
	fs := afero.NewMemMapFs()
	sessionsDir := "/state/" + sessionsSubdir
	if err := fs.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 20; i++ {
		name := filepath.Join(sessionsDir, sessionFilePrefix+
			base.Add(time.Duration(i)*time.Hour).Format("20060102-150405")+
			"-0000-000000000"+sessionFileSuffix)
		f, err := fs.Create(name)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		_ = f.Close()
		if err := fs.Chtimes(name, base.Add(time.Duration(i)*time.Hour), base.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}

	clk := &fakeClock{now: base.Add(100 * time.Hour)}
	_, closer, err := Open(Options{Dir: "/state", FS: fs, Clock: clk, RetentionCount: 20, Pid: 1234})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = closer.Close() }()

	names := readSessionFiles(t, fs, sessionsDir)
	if len(names) != 20 {
		t.Fatalf("expected 20 files post-prune, got %d: %v", len(names), names)
	}
}

func TestOpen_RetentionSkipsSymlink(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("symlink semantics: linux/darwin")
	}

	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, sessionsSubdir)
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 20; i++ {
		name := filepath.Join(sessionsDir, sessionFilePrefix+
			base.Add(time.Duration(i)*time.Hour).Format("20060102-150405")+
			"-0000-000000000"+sessionFileSuffix)
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := os.Chtimes(name, base.Add(time.Duration(i)*time.Hour), base.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}
	symPath := filepath.Join(sessionsDir, sessionFilePrefix+"99999999-999999-0000-000000000"+sessionFileSuffix)
	if err := os.Symlink("/tmp/nonexistent-target.log", symPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	clk := &fakeClock{now: base.Add(100 * time.Hour)}
	_, closer, err := Open(Options{Dir: tmp, Clock: clk, RetentionCount: 20, Pid: 1234})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = closer.Close() }()

	if _, lerr := os.Lstat(symPath); lerr != nil {
		t.Fatalf("symlink got pruned: %v", lerr)
	}
}

func TestOpen_RefusesSymlinkAtPath_ELOOP(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("ELOOP/O_NOFOLLOW: linux/darwin")
	}

	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, sessionsSubdir)
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	clk := &fakeClock{now: time.Date(2026, 5, 21, 12, 0, 0, 123, time.UTC)}
	expectedName := sessionFilePrefix +
		clk.now.Format("20060102-150405") + "-1234-" +
		formatNano(clk.now.UnixNano()%1_000_000_000) + sessionFileSuffix

	elsewhere := filepath.Join(tmp, "elsewhere-target.log")
	target := filepath.Join(sessionsDir, expectedName)
	if err := os.Symlink(elsewhere, target); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, _, err := Open(Options{Dir: tmp, Clock: clk, Pid: 1234})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "refusing symlinked target") {
		t.Fatalf("error %q does not mention refusal", err.Error())
	}
	if _, ferr := os.Stat(elsewhere); ferr == nil {
		t.Fatal("symlink target was created — O_NOFOLLOW failed")
	}
}

// formatNano mirrors the %09d formatting used in session_logger.go.
func formatNano(n int64) string {
	s := []byte("000000000")
	idx := len(s) - 1
	for n > 0 && idx >= 0 {
		s[idx] = byte('0' + n%10)
		n /= 10
		idx--
	}
	return string(s)
}

func TestOpen_EnforcesMode0600OnExistingFile(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("unix perms")
	}
	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, sessionsSubdir)
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	clk := &fakeClock{now: time.Date(2026, 5, 21, 12, 0, 0, 456, time.UTC)}
	expectedName := sessionFilePrefix +
		clk.now.Format("20060102-150405") + "-1234-" +
		formatNano(clk.now.UnixNano()%1_000_000_000) + sessionFileSuffix
	target := filepath.Join(sessionsDir, expectedName)
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, closer, err := Open(Options{Dir: tmp, Clock: clk, Pid: 1234})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = closer.Close() }()

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("perm = %o, want 0600", got)
	}
}

func TestOpen_LevelSplit_WarnGoesToStderr(t *testing.T) {
	fs := afero.NewMemMapFs()
	var stderr bytes.Buffer
	clk := &fakeClock{now: time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)}

	l, closer, err := Open(Options{Dir: "/state", FS: fs, Clock: clk, Stderr: &stderr, Pid: 1})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = closer.Close() }()

	Event(l, "x", "dbg")
	l.LogAttrs(context.Background(), slog.LevelWarn, "something",
		slog.String("cat", "x"),
		slog.String("evt", "warn"),
	)

	names := readSessionFiles(t, fs, "/state/"+sessionsSubdir)
	if len(names) != 1 {
		t.Fatalf("expected 1 file, got %v", names)
	}
	f, err := fs.Open(filepath.Join("/state/"+sessionsSubdir, names[0]))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()
	content, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	cs := string(content)
	if !strings.Contains(cs, `"evt":"dbg"`) {
		t.Fatalf("file missing debug entry: %s", cs)
	}
	if !strings.Contains(cs, `"evt":"warn"`) {
		t.Fatalf("file missing warn entry: %s", cs)
	}

	se := stderr.String()
	if strings.Contains(se, "evt=dbg") {
		t.Fatalf("stderr contains debug line: %s", se)
	}
	if !strings.Contains(se, "evt=warn") && !strings.Contains(se, "something") {
		t.Fatalf("stderr missing warn line: %s", se)
	}
}

func TestOpen_CategoryFilter(t *testing.T) {
	fs := afero.NewMemMapFs()
	clk := &fakeClock{now: time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)}

	l, closer, err := Open(Options{
		Dir:        "/state",
		FS:         fs,
		Clock:      clk,
		Categories: []string{"db"},
		Pid:        1,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = closer.Close() }()

	Event(l, "db", "query")
	Event(l, "input", "key")

	names := readSessionFiles(t, fs, "/state/"+sessionsSubdir)
	f, _ := fs.Open(filepath.Join("/state/"+sessionsSubdir, names[0]))
	defer func() { _ = f.Close() }()
	content, _ := io.ReadAll(f)
	cs := string(content)

	if !strings.Contains(cs, `"evt":"query"`) {
		t.Fatalf("missing db line: %s", cs)
	}
	if strings.Contains(cs, `"evt":"key"`) {
		t.Fatalf("input line leaked past filter: %s", cs)
	}
	if strings.Contains(cs, `"evt":"startup_marker"`) {
		t.Fatalf("startup_marker leaked past filter: %s", cs)
	}
}

func TestOpen_StartupMarker_FirstLine(t *testing.T) {
	fs := afero.NewMemMapFs()
	clk := &fakeClock{now: time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)}

	_, closer, err := Open(Options{
		Dir:   "/state",
		FS:    fs,
		Clock: clk,
		Pid:   1,
		BuildInfo: BuildInfo{
			Version: "1.2.3",
			Commit:  "abcdef",
			Date:    "2026-05-21",
		},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = closer.Close() }()

	names := readSessionFiles(t, fs, "/state/"+sessionsSubdir)
	f, _ := fs.Open(filepath.Join("/state/"+sessionsSubdir, names[0]))
	defer func() { _ = f.Close() }()
	content, _ := io.ReadAll(f)

	firstLine := strings.SplitN(strings.TrimSpace(string(content)), "\n", 2)[0]
	var obj map[string]any
	if err := json.Unmarshal([]byte(firstLine), &obj); err != nil {
		t.Fatalf("first line is not JSON: %q (%v)", firstLine, err)
	}
	if obj["evt"] != "startup_marker" {
		t.Fatalf("first line evt = %v, want startup_marker", obj["evt"])
	}
	if obj["version"] != "1.2.3" {
		t.Fatalf("version = %v, want 1.2.3", obj["version"])
	}
	if obj["commit"] != "abcdef" {
		t.Fatalf("commit = %v, want abcdef", obj["commit"])
	}
}

// writeErroringFs wraps a MemMapFs to inject ENOSPC on Write.
type writeErroringFs struct {
	afero.Fs
}

func (w writeErroringFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	f, err := w.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &enospcFile{File: f}, nil
}

type enospcFile struct{ afero.File }

func (f *enospcFile) Write(_ []byte) (int, error) { return 0, syscall.ENOSPC }

func TestOpen_DiskFullReturnsSilent(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic on disk full: %v", r)
		}
	}()
	fs := writeErroringFs{Fs: afero.NewMemMapFs()}
	clk := &fakeClock{now: time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)}

	l, closer, err := Open(Options{Dir: "/state", FS: fs, Clock: clk, Pid: 1})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	l.Info("won't make it to disk")
	if cerr := closer.Close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}
	if cerr := closer.Close(); cerr != nil {
		t.Fatalf("second close: %v", cerr)
	}
}

func TestOpen_FsAcceptsMemMapFs(t *testing.T) {
	fs := afero.NewMemMapFs()
	l, closer, err := Open(Options{Dir: "/state", FS: fs})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if l == nil || closer == nil {
		t.Fatal("nil logger or closer")
	}
	_ = closer.Close()
}

// blockingCloseFile wraps an afero.File so that Close() blocks until the test
// signals it. Used to exercise CloseWithDeadline timeouts.
type blockingCloseFile struct {
	afero.File
	release chan struct{}
}

func (b *blockingCloseFile) Close() error {
	<-b.release
	return b.File.Close()
}

// blockingCloseFs wraps a MemMapFs and yields blockingCloseFile from OpenFile.
type blockingCloseFs struct {
	afero.Fs
	release chan struct{}
}

func (b *blockingCloseFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	f, err := b.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &blockingCloseFile{File: f, release: b.release}, nil
}

func TestSessionCloser_ForceClosesOnDeadline(t *testing.T) {
	release := make(chan struct{})
	defer close(release) // ensure the blocked Close eventually returns so goroutines don't leak

	fs := &blockingCloseFs{Fs: afero.NewMemMapFs(), release: release}
	clk := &fakeClock{now: time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)}

	_, closer, err := Open(Options{Dir: "/state", FS: fs, Clock: clk, Pid: 1})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	start := time.Now()
	cerr := closer.CloseWithDeadline(50 * time.Millisecond)
	elapsed := time.Since(start)

	if cerr == nil {
		t.Fatal("expected deadline-exceeded error, got nil")
	}
	if !strings.Contains(cerr.Error(), "close deadline exceeded") {
		t.Fatalf("error %q does not mention deadline", cerr.Error())
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("CloseWithDeadline took too long: %v", elapsed)
	}
}

func TestOpen_ReplaysPruneWarnings(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("symlink semantics: linux/darwin")
	}

	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, sessionsSubdir)
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 20; i++ {
		name := filepath.Join(sessionsDir, sessionFilePrefix+
			base.Add(time.Duration(i)*time.Hour).Format("20060102-150405")+
			"-0000-000000000"+sessionFileSuffix)
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	symPath := filepath.Join(sessionsDir, sessionFilePrefix+"99999999-999999-0000-000000000"+sessionFileSuffix)
	if err := os.Symlink("/tmp/nonexistent-target.log", symPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	clk := &fakeClock{now: base.Add(100 * time.Hour)}
	_, closer, err := Open(Options{Dir: tmp, Clock: clk, RetentionCount: 20, Pid: 1234})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = closer.Close() }()

	// Find the newly opened session file and inspect contents.
	names := readSessionFiles(t, afero.NewOsFs(), sessionsDir)
	// Find the freshly created one (matches pid 1234).
	var target string
	for _, n := range names {
		if strings.Contains(n, "-1234-") {
			target = filepath.Join(sessionsDir, n)
			break
		}
	}
	if target == "" {
		t.Fatalf("could not locate fresh session file in %v", names)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("readfile: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected >=2 lines, got %d: %s", len(lines), string(content))
	}

	// First line must be the startup marker.
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("first line not JSON: %v", err)
	}
	if first["evt"] != "startup_marker" {
		t.Fatalf("first line evt = %v, want startup_marker", first["evt"])
	}

	// At least one subsequent line must be a retention_warn at WARN level.
	found := false
	for _, ln := range lines[1:] {
		var obj map[string]any
		if err := json.Unmarshal([]byte(ln), &obj); err != nil {
			continue
		}
		if obj["evt"] == "retention_warn" && obj["level"] == "WARN" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no retention_warn line found in:\n%s", string(content))
	}
}
