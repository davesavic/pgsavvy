package config

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/afero"
)

const connectionsLockTimeout = 250 * time.Millisecond

// Virtual filesystems must explicitly supply their own per-instance protocol.
// Wrappers around native filesystems must instead delegate to the native lock;
// an unknown filesystem never silently gets process-only synchronization.
type connectionsWriteLocker interface {
	LockConnections(path string, deadline time.Time) (canonical string, release func(), err error)
}

type connectionsLockError struct{ cause error }

func (e *connectionsLockError) Error() string {
	return "config: connections write lock unavailable; retry"
}

func (e *connectionsLockError) Unwrap() error   { return e.cause }
func (e *connectionsLockError) Temporary() bool { return true }

func withConnectionsWriteLock(fs afero.Fs, path string, write func(string) error) error {
	canonical, release, err := acquireConnectionsWriteLock(fs, path, time.Now().Add(connectionsLockTimeout))
	if err != nil {
		// Do not expose filesystem paths or backend diagnostics to callers.
		return &connectionsLockError{cause: err}
	}
	defer release()
	return write(canonical)
}

func acquireConnectionsWriteLock(fs afero.Fs, path string, deadline time.Time) (string, func(), error) {
	if _, ok := fs.(*afero.OsFs); ok {
		return nativeConnectionsWriteLock(path, deadline)
	}
	if locker, ok := fs.(connectionsWriteLocker); ok {
		return locker.LockConnections(path, deadline)
	}
	return "", nil, errors.New("unsupported connections filesystem locking")
}

func nativeConnectionsWriteLock(path string, deadline time.Time) (string, func(), error) {
	path, err := canonicalConnectionsPath(path)
	if err != nil {
		return "", nil, err
	}
	if err := validateConnectionsTarget(path); err != nil {
		return "", nil, err
	}
	file, err := openConnectionsLock(path + ".lock")
	if err != nil {
		return "", nil, err
	}
	locked := false
	defer func() {
		if !locked {
			_ = file.Close()
		}
	}()
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return "", nil, os.ErrDeadlineExceeded
		}
		busy, err := tryConnectionsLock(file)
		if err != nil {
			return "", nil, err
		}
		if !busy {
			break
		}
		time.Sleep(min(5*time.Millisecond, remaining))
	}
	info, err := file.Stat()
	if err != nil {
		return "", nil, err
	}
	named, err := os.Lstat(path + ".lock")
	if err != nil {
		return "", nil, err
	}
	if !os.SameFile(info, named) || !safeConnectionsFile(info) {
		return "", nil, errors.New("unsafe connections lock")
	}
	if err := validateConnectionsTarget(path); err != nil {
		return "", nil, err
	}
	if time.Until(deadline) <= 0 {
		return "", nil, os.ErrDeadlineExceeded
	}
	locked = true
	// Closing the owned handle releases kernel ownership, including on errors.
	// Never unlink: waiters may already hold handles to this exact inode.
	return path, func() { _ = file.Close() }, nil
}

func canonicalConnectionsPath(path string) (string, error) {
	// Abs/Dir clean .. lexically, changing its meaning after a symlink.
	// Resolve the raw parent first so reads, writes and locks share a target.
	parent, base := filepath.Split(path)
	if base == "" || base == "." || base == ".." {
		return "", errors.New("invalid connections basename")
	}
	if err := validateConnectionsBasename(base); err != nil {
		return "", err
	}
	if parent == "" {
		parent = "."
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", err
	}
	parent, err = filepath.Abs(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, base), nil
}

func validateConnectionsTarget(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !safeConnectionsFile(info) {
		return errors.New("unsafe connections target")
	}
	return validateConnectionsLinks(path)
}
