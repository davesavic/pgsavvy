//go:build linux || darwin

package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestConnectionsWriteLock_SymlinkDotDot(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	require.NoError(t, os.MkdirAll(a, 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(b, "subdir"), 0o700))
	require.NoError(t, os.Symlink(filepath.Join(b, "subdir"), filepath.Join(a, "link")))
	before := []byte("connections:\n - name: dev\n   cli_history: true\n")
	intended, other := filepath.Join(b, "connections.yml"), filepath.Join(a, "connections.yml")
	for _, path := range []string{intended, other} {
		require.NoError(t, os.WriteFile(path, before, 0o600))
	}
	// Do not use Join: the literal .. must reach the filesystem after the symlink.
	raw := a + "/link/../connections.yml"
	fs := afero.NewOsFs()
	conns, err := LoadConnections(fs, raw)
	require.NoError(t, err)
	require.True(t, conns[0].CLIHistory)
	conns[0].CLIHistory = false
	expected := true
	require.NoError(t, UpdateConnectionWithCLISettings(fs, raw, "dev", conns[0], nil, &expected))
	conns, err = LoadConnections(fs, intended)
	require.NoError(t, err)
	require.False(t, conns[0].CLIHistory, "revoke the profile that was actually read")
	unchanged, err := os.ReadFile(other)
	require.NoError(t, err)
	require.Equal(t, before, unchanged)
	canonical, release, err := nativeConnectionsWriteLock(raw, time.Now().Add(time.Second))
	require.NoError(t, err)
	defer release()
	require.Equal(t, intended, canonical)
	require.ErrorIs(t, SaveConnections(fs, intended, nil), os.ErrDeadlineExceeded, "physical alias must share the lock")
}

func TestConnectionsWriteLock_PermissionDenial(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission denial requires an unprivileged process")
	}
	for _, target := range []string{"directory", "lock"} {
		t.Run(target, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "password=LOCK_ERROR_SENTINEL")
			path := filepath.Join(dir, "connections.yml")
			fs := afero.NewOsFs()
			if err := SaveConnections(fs, path, nil); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			denied, mode := dir, os.FileMode(0)
			if target == "lock" {
				denied, mode = path+".lock", 0o400
			}
			info, err := os.Stat(denied)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.Chmod(denied, info.Mode().Perm()); err != nil {
					t.Error(err)
				}
			})
			if err := os.Chmod(denied, mode); err != nil {
				t.Fatal(err)
			}
			err = SaveConnections(fs, path, nil)
			var retryable interface{ Temporary() bool }
			if !errors.Is(err, os.ErrPermission) || !errors.As(err, &retryable) || !retryable.Temporary() {
				t.Fatalf("denial not retryable: %v", err)
			}
			if strings.Contains(err.Error(), "LOCK_ERROR_SENTINEL") {
				t.Fatal("lock error exposed path diagnostics")
			}
			if err := os.Chmod(denied, info.Mode().Perm()); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("permission denial changed profiles")
			}
			if err := SaveConnections(fs, path, nil); err != nil {
				t.Fatalf("denial retained ownership: %v", err)
			}
		})
	}
}

func TestConnectionsWriteLock_NonregularSidecar(t *testing.T) {
	for _, kind := range []string{"fifo", "directory", "insecure-file"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "connections.yml")
			sidecar := path + ".lock"
			var err error
			switch kind {
			case "fifo":
				err = unix.Mkfifo(sidecar, 0o600)
			case "directory":
				err = os.Mkdir(sidecar, 0o700)
			case "insecure-file":
				err = os.WriteFile(sidecar, []byte("outside"), 0o644)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(sidecar)
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			if err := SaveConnections(afero.NewOsFs(), path, nil); err == nil {
				t.Fatal("unsafe sidecar accepted")
			}
			if time.Since(start) > 750*time.Millisecond {
				t.Fatal("unsafe sidecar acquisition blocked")
			}
			after, err := os.Lstat(sidecar)
			if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatalf("unsafe sidecar replaced or repaired: %v", err)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("profile created on lock denial: %v", err)
			}
		})
	}
}
