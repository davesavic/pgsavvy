package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/models"
)

type connectionsTestFs struct {
	afero.Fs
	lock chan struct{}
}

func newConnectionsTestFs() *connectionsTestFs {
	return &connectionsTestFs{Fs: afero.NewMemMapFs(), lock: make(chan struct{}, 1)}
}

func (fs *connectionsTestFs) LockConnections(path string, deadline time.Time) (string, func(), error) {
	timer := time.NewTimer(max(0, time.Until(deadline)))
	defer timer.Stop()
	select {
	case fs.lock <- struct{}{}:
		return filepath.Clean(path), func() { <-fs.lock }, nil
	case <-timer.C:
		return "", nil, os.ErrDeadlineExceeded
	}
}

func (fs *renameFailFs) LockConnections(path string, deadline time.Time) (string, func(), error) {
	return fs.Fs.(connectionsWriteLocker).LockConnections(path, deadline)
}

func (fs *changingConnectionFs) LockConnections(path string, deadline time.Time) (string, func(), error) {
	return fs.Fs.(connectionsWriteLocker).LockConnections(path, deadline)
}

func TestConnectionsWriteLock_ExplicitVirtualFilesystem(t *testing.T) {
	raw := afero.NewMemMapFs()
	before := []byte("connections: []\n")
	if err := afero.WriteFile(raw, "/c.yml", before, 0o600); err != nil {
		t.Fatal(err)
	}
	err := SaveConnections(raw, "/c.yml", nil)
	var retryable interface{ Temporary() bool }
	if !errors.As(err, &retryable) || !retryable.Temporary() {
		t.Fatalf("unknown filesystem did not fail closed: %v", err)
	}
	after, _ := afero.ReadFile(raw, "/c.yml")
	if !bytes.Equal(before, after) {
		t.Fatal("unsupported filesystem changed profiles")
	}
	fs := newConnectionsTestFs()
	_, release, err := fs.LockConnections("/c.yml", time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	err = SaveConnections(fs, "/c.yml", nil)
	release()
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("contention: %v", err)
	}
	if err := SaveConnections(fs, "/c.yml", nil); err != nil {
		t.Fatal(err)
	}
}

func TestConnectionsWriteLock_WriteFailureRelease(t *testing.T) {
	base := newConnectionsTestFs()
	fs := &renameFailFs{Fs: base}
	if err := SaveConnections(fs, "/c.yml", nil); err == nil {
		t.Fatal("rename failure lost")
	}
	entries, err := afero.ReadDir(base, "/")
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed publication leaked temporary: %v, %v", entries, err)
	}
	if err := AppendConnection(base, "/c.yml", models.Connection{Name: "dev"}); err != nil {
		t.Fatalf("failure retained lock: %v", err)
	}
}
