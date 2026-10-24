//go:build linux || darwin

package machineio

import (
	"context"
	"io"
	"os"
	"runtime"
	"strconv"
	"sync"

	"golang.org/x/sys/unix"
)

// OpenApprovalTerminal owns an independent controlling-terminal description and
// the persistent per-user ceremony lock. The caller owns cancellation and Close.
func OpenApprovalTerminal(ctx context.Context) (io.ReadWriteCloser, error) {
	dir := "/tmp"
	if runtime.GOOS == "darwin" {
		dir = "/private/tmp"
	}
	return openApprovalTerminal(ctx, dir, "pgsavvy-approval-"+strconv.Itoa(os.Geteuid())+".lock")
}

func openApprovalTerminal(ctx context.Context, dir, name string) (io.ReadWriteCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lock, err := openApprovalLock(dir, name)
	if err != nil {
		return nil, errUnavailable
	}
	fd, err := unix.Open("/dev/tty", unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOCTTY|unix.O_NOFOLLOW, 0)
	if err != nil {
		_ = unix.Close(lock)
		return nil, errUnavailable
	}
	terminal := &approvalTerminal{pollStream: &pollStream{fd: fd, closed: make(chan struct{})}, lock: lock}
	var info unix.Stat_t
	_, ttyErr := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
	if unix.Fstat(fd, &info) != nil || info.Mode&unix.S_IFMT != unix.S_IFCHR || ttyErr != nil || ctx.Err() != nil {
		_ = terminal.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errUnavailable
	}
	return terminal, nil
}

type approvalTerminal struct {
	*pollStream
	lock int
	once sync.Once
	err  error
}

func (f *approvalTerminal) Close() error {
	f.once.Do(func() {
		f.err = f.pollStream.Close()
		if unix.Close(f.lock) != nil {
			f.err = errUnavailable
		}
	})
	return f.err
}

func openApprovalLock(dir, name string) (int, error) {
	directory, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, errUnavailable
	}
	defer func() { _ = unix.Close(directory) }()
	var parent unix.Stat_t
	if unix.Fstat(directory, &parent) != nil {
		return -1, errUnavailable
	}
	// Only root's protected namespace or a private owner-controlled fixture.
	trusted := parent.Uid == 0 && (parent.Mode&0o022 == 0 || parent.Mode&unix.S_ISVTX != 0) || parent.Uid == uint32(os.Geteuid()) && parent.Mode&0o077 == 0
	if !trusted {
		return -1, errUnavailable
	}
	fd, err := unix.Openat(directory, name, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return -1, errUnavailable
	}
	var info, current unix.Stat_t
	if unix.Fstat(fd, &info) != nil || !safeApprovalLock(info) || unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB) != nil || unix.Fstatat(directory, name, &current, unix.AT_SYMLINK_NOFOLLOW) != nil || info.Dev != current.Dev || info.Ino != current.Ino {
		_ = unix.Close(fd)
		return -1, errUnavailable
	}
	return fd, nil
}

func safeApprovalLock(info unix.Stat_t) bool {
	return info.Mode&unix.S_IFMT == unix.S_IFREG && info.Mode&0o7777 == 0o600 && info.Uid == uint32(os.Geteuid()) && info.Nlink == 1
}
