//go:build linux || darwin

package config

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func openConnectionsLock(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !safeConnectionsFile(info) || info.Mode().Perm()&0o077 != 0 {
		_ = file.Close()
		return nil, errors.New("unsafe connections lock")
	}
	return file, nil
}

func safeConnectionsFile(info os.FileInfo) bool {
	if info == nil || !info.Mode().IsRegular() {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1
}

func validateConnectionsLinks(string) error { return nil }

func validateConnectionsBasename(string) error { return nil }

func tryConnectionsLock(file *os.File) (busy bool, err error) {
	err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EINTR) {
		return true, nil
	}
	return false, err
}
