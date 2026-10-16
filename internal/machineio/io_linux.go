package machineio

import (
	"context"
	"io"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

func openStream(ctx context.Context, file *os.File, mode int) (io.ReadWriteCloser, error) {
	raw, err := file.SyscallConn()
	if err != nil {
		return nil, err
	}
	owned := -1
	socket := false
	var setupErr error
	err = raw.Control(func(fd uintptr) {
		if setupErr = ctx.Err(); setupErr != nil {
			return
		}
		var info unix.Stat_t
		if setupErr = unix.Fstat(int(fd), &info); setupErr != nil {
			return
		}
		kind := info.Mode & unix.S_IFMT
		if kind != unix.S_IFREG && kind != unix.S_IFCHR && kind != unix.S_IFIFO && kind != unix.S_IFSOCK {
			setupErr = errUnavailable
			return
		}
		flags, flagErr := unix.FcntlInt(fd, unix.F_GETFL, 0)
		access := flags & unix.O_ACCMODE
		if flagErr != nil || flags&unix.O_PATH != 0 || mode == os.O_RDONLY && access == unix.O_WRONLY || mode == os.O_WRONLY && access == unix.O_RDONLY {
			setupErr = errUnavailable
			return
		}
		source := int(fd)
		socket = kind == unix.S_IFSOCK
		if socket {
			if setupErr = checkStreamSocket(source); setupErr != nil {
				return
			}
		}
		if kind == unix.S_IFIFO && flags&unix.O_DIRECT != 0 {
			setupErr = errUnavailable
			return
		}
		if kind == unix.S_IFCHR || kind == unix.S_IFIFO {
			// Reopening a PTY master allocates another PTY, not another description
			// of the inherited terminal. Unsupported/unverifiable devices fail closed.
			if kind == unix.S_IFCHR {
				if _, ioctlErr := unix.IoctlGetInt(int(fd), unix.TIOCGPTN); ioctlErr != unix.ENOTTY {
					setupErr = errUnavailable
					return
				}
			}
			source, setupErr = reopenLinuxStream(source, &info, mode)
			if setupErr != nil {
				return
			}
			defer func() { _ = unix.Close(source) }()
		}
		owned, setupErr = unix.FcntlInt(uintptr(source), unix.F_DUPFD_CLOEXEC, 3)
		if setupErr != nil {
			return
		}
	})
	if err != nil || setupErr != nil {
		if owned >= 0 {
			_ = unix.Close(owned)
		}
		return nil, errUnavailable
	}
	if socket {
		return &pollStream{fd: owned, socket: true, closed: make(chan struct{})}, nil
	}
	return os.NewFile(uintptr(owned), "pgsavvy-machine-stdio"), nil
}

// The reopen creates a private open-file description; dup alone shares flags.
// Keep the borrowed fd protected by Control until identity/access are checked.
func reopenLinuxStream(source int, original *unix.Stat_t, mode int) (int, error) {
	fd, err := unix.Open("/proc/self/fd/"+strconv.Itoa(source), mode|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOCTTY, 0)
	if err != nil {
		return -1, errUnavailable
	}
	var opened unix.Stat_t
	statErr := unix.Fstat(fd, &opened)
	flags, flagErr := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if statErr != nil || flagErr != nil || opened.Dev != original.Dev || opened.Ino != original.Ino || opened.Mode&unix.S_IFMT != original.Mode&unix.S_IFMT || opened.Rdev != original.Rdev || flags&unix.O_ACCMODE != mode || flags&unix.O_NONBLOCK == 0 {
		_ = unix.Close(fd)
		return -1, errUnavailable
	}
	return fd, nil
}
