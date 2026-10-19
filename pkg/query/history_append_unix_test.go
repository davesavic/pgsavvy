//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package query

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHistoryAppend_TUIFirstPermissions(t *testing.T) {
	for _, first := range []string{"tui", "cli"} {
		t.Run(first, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "private-state", "history.sqlite")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHistoryAppend_UmaskHelper$")
			cmd.Env = append(os.Environ(), "PGSAVVY_UMASK_TEST_PATH="+path, "PGSAVVY_UMASK_TEST_FIRST="+first)
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, string(output))
		})
	}
}

func TestHistoryAppend_UmaskHelper(t *testing.T) {
	path := os.Getenv("PGSAVVY_UMASK_TEST_PATH")
	if path == "" {
		return
	}
	syscall.Umask(0o022)
	if os.Getenv("PGSAVVY_UMASK_TEST_FIRST") == "cli" {
		require.NoError(t, appendWithin(t, path))
	}
	h, err := New(path)
	require.NoError(t, err)
	defer func() { _ = h.Close() }()
	// A merely opened SQLite handle need not participate in WAL yet. Exercise
	// the TUI's actual batch writer before inspecting its live sidecars.
	require.NoError(t, h.flushBatch([]historyEntry{{executedAt: 1, sql: "SELECT tui", source: "tui", succeeded: true}}))
	assertPrivateHistoryArtifacts(t, path)
	require.NoError(t, appendWithin(t, path))
	assertPrivateHistoryArtifacts(t, path)
	require.NoError(t, h.Close())
}

func assertPrivateHistoryArtifacts(t *testing.T, path string) {
	t.Helper()
	for _, artifact := range []string{filepath.Dir(path), path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(artifact)
		require.NoError(t, err)
		want := os.FileMode(0o600)
		if artifact == filepath.Dir(path) {
			want = 0o700
		}
		require.Equal(t, want, info.Mode().Perm(), artifact)
	}
}

func TestHistoryAppend_UnsafePermissions(t *testing.T) {
	for _, fixture := range []string{"broad_db", "broad_wal", "writable_directory", "hardlink", "fifo", "read_only_db", "read_only_directory"} {
		t.Run(fixture, func(t *testing.T) {
			if (fixture == "read_only_db" || fixture == "read_only_directory") && os.Geteuid() == 0 {
				t.Skip("permission-denial fixture must run unprivileged")
			}
			dir := t.TempDir()
			path := filepath.Join(dir, "history.sqlite")
			outside := filepath.Join(t.TempDir(), "outside.sqlite")
			require.NoError(t, appendWithin(t, outside))
			outsideBefore, err := os.ReadFile(outside)
			require.NoError(t, err)
			var before []byte
			switch fixture {
			case "broad_db", "broad_wal", "read_only_db":
				require.NoError(t, appendWithin(t, path))
				before, err = os.ReadFile(path)
				require.NoError(t, err)
				switch fixture {
				case "broad_db":
					require.NoError(t, os.Chmod(path, 0o644))
				case "broad_wal":
					require.NoError(t, os.WriteFile(path+"-wal", []byte("sidecar sentinel"), 0o644))
				default:
					require.NoError(t, os.Chmod(path, 0o400))
				}
			case "writable_directory":
				require.NoError(t, os.Chmod(dir, 0o777))
			case "read_only_directory":
				require.NoError(t, os.Chmod(dir, 0o500))
				t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
			case "hardlink":
				require.NoError(t, os.Link(outside, path))
			case "fifo":
				require.NoError(t, syscall.Mkfifo(path, 0o600))
			}
			start := time.Now()
			require.ErrorIs(t, appendWithin(t, path), ErrHistoryUnavailable)
			require.Less(t, time.Since(start), 250*time.Millisecond)
			if before != nil {
				after, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, before, after)
			}
			outsideAfter, err := os.ReadFile(outside)
			require.NoError(t, err)
			require.Equal(t, outsideBefore, outsideAfter)
			if fixture == "broad_db" {
				info, err := os.Stat(path)
				require.NoError(t, err)
				require.Equal(t, os.FileMode(0o644), info.Mode().Perm())
				// Compatibility: the TUI does not adopt CLI privacy refusal or
				// silently repair a pre-existing broad-access store.
				h, err := New(path)
				require.NoError(t, err)
				require.NoError(t, h.Close())
				info, err = os.Stat(path)
				require.NoError(t, err)
				require.Equal(t, os.FileMode(0o644), info.Mode().Perm())
			}
			if fixture == "broad_wal" {
				data, err := os.ReadFile(path + "-wal")
				require.NoError(t, err)
				require.Equal(t, "sidecar sentinel", string(data))
			}
		})
	}
	// Ownership and link checks are independent of mode-bit validation.
	info, err := os.Stat(t.TempDir())
	require.NoError(t, err)
	stat := *info.Sys().(*syscall.Stat_t)
	stat.Uid++
	require.False(t, safeHistoryDirectory(historyOwnedInfo{FileInfo: info, stat: &stat}, true))
}

type historyOwnedInfo struct {
	os.FileInfo
	stat *syscall.Stat_t
}

func (i historyOwnedInfo) Sys() any { return i.stat }

func TestHistoryAppend_AncestorAlias(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "real")
	require.NoError(t, os.Mkdir(dir, 0o700))
	alias := filepath.Join(parent, "alias")
	require.NoError(t, os.Symlink(dir, alias))
	require.NoError(t, appendWithin(t, filepath.Join(alias, "history.sqlite")))
	db, err := sql.Open("sqlite", filepath.Join(dir, "history.sqlite"))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM history`).Scan(&count))
	require.Equal(t, 1, count)
}

func TestHistoryAppend_ReplaceableAncestor(t *testing.T) {
	ancestor := filepath.Join(t.TempDir(), "untrusted-parent")
	require.NoError(t, os.Mkdir(ancestor, 0o777))
	require.NoError(t, os.Chmod(ancestor, 0o777))
	leaf := filepath.Join(ancestor, "private-leaf")
	require.NoError(t, os.Mkdir(leaf, 0o700))
	// Reject before either opening an existing private leaf or creating one.
	for _, dir := range []string{leaf, filepath.Join(ancestor, "missing-leaf")} {
		require.ErrorIs(t, appendWithin(t, filepath.Join(dir, "history.sqlite")), ErrHistoryUnavailable)
		_, err := os.Stat(filepath.Join(dir, "history.sqlite"))
		require.ErrorIs(t, err, os.ErrNotExist)
	}
	_, err := os.Stat(filepath.Join(ancestor, "missing-leaf"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestHistoryAppend_ConcurrentDirectoryCreation(t *testing.T) {
	parent := t.TempDir()
	for _, fixture := range []string{"private", "writable", "symlink"} {
		t.Run(fixture, func(t *testing.T) {
			path := filepath.Join(parent, fixture)
			// Simulate a component appearing after historyDirectory's ancestor
			// preflight but before its exclusive Mkdir. Exercise that exact boundary.
			if fixture == "symlink" {
				require.NoError(t, os.Symlink(t.TempDir(), path))
			} else {
				require.NoError(t, os.Mkdir(path, 0o700))
				if fixture == "writable" {
					require.NoError(t, os.Chmod(path, 0o777))
				}
			}
			err := makeHistoryDirectory(path, true)
			if fixture == "private" {
				require.NoError(t, err, "cooperating creators share a private directory")
			} else {
				require.ErrorIs(t, err, ErrHistoryUnavailable)
			}
		})
	}
}

func TestHistoryAppend_AncestorSubstitution(t *testing.T) {
	parent := t.TempDir()
	ancestor := filepath.Join(parent, "resolved")
	require.NoError(t, os.Mkdir(ancestor, 0o700))
	resolved, err := filepath.EvalSymlinks(ancestor)
	require.NoError(t, err)
	// Replace the formerly canonical component after resolution but before
	// ancestry validation. Stat would hide the replacement link's ownership.
	require.NoError(t, os.Rename(ancestor, ancestor+"-original"))
	require.NoError(t, os.Symlink(ancestor+"-original", ancestor))
	require.ErrorIs(t, validateHistoryAncestors(resolved, true), ErrHistoryUnavailable)
}
