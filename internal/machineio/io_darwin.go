package machineio

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func openStream(ctx context.Context, file *os.File, mode int) (io.ReadWriteCloser, error) {
	raw, err := file.SyscallConn()
	if err != nil {
		return nil, err
	}
	owned := -1
	regular := false
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
		flags, flagErr := unix.FcntlInt(fd, unix.F_GETFL, 0)
		access := flags & unix.O_ACCMODE
		if flagErr != nil || flags&unix.O_EVTONLY != 0 || mode == os.O_RDONLY && access == unix.O_WRONLY || mode == os.O_WRONLY && access == unix.O_RDONLY {
			setupErr = errUnavailable
			return
		}
		regular = info.Mode&unix.S_IFMT == unix.S_IFREG
		if info.Mode&unix.S_IFMT == unix.S_IFIFO && flags&unix.O_NONBLOCK == 0 {
			setupErr = errUnavailable
			return
		}
		source := int(fd)
		socket = info.Mode&unix.S_IFMT == unix.S_IFSOCK
		if socket {
			if setupErr = checkStreamSocket(source); setupErr != nil {
				return
			}
			// MSG_DONTWAIT does not prevent Darwin sends waiting for buffer
			// space. A duplicate cannot change O_NONBLOCK independently.
			if mode == os.O_WRONLY && flags&unix.O_NONBLOCK == 0 {
				setupErr = errUnavailable
				return
			}
		}
		if info.Mode&unix.S_IFMT == unix.S_IFCHR {
			source, setupErr = reopenCharacter(ctx, &info, mode)
			if setupErr != nil {
				return
			}
			defer func() { _ = unix.Close(source) }()
		} else if !regular && info.Mode&unix.S_IFMT != unix.S_IFIFO && info.Mode&unix.S_IFMT != unix.S_IFSOCK {
			setupErr = errUnavailable
			return
		}
		owned, setupErr = unix.FcntlInt(uintptr(source), unix.F_DUPFD_CLOEXEC, 3)
	})
	if err != nil || setupErr != nil {
		if owned >= 0 {
			_ = unix.Close(owned)
		}
		return nil, errUnavailable
	}
	if regular {
		return os.NewFile(uintptr(owned), "pgsavvy-machine-stdio"), nil
	}
	return &pollStream{fd: owned, socket: socket, closed: make(chan struct{})}, nil
}

func reopenCharacter(ctx context.Context, original *unix.Stat_t, mode int) (int, error) {
	entries, err := os.ReadDir("/dev")
	if err != nil {
		return -1, err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return -1, err
		}
		// Multiplexer opens create a new PTY rather than the inherited terminal.
		if strings.HasPrefix(entry.Name(), "pty") || entry.Name() == "ptmx" || entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			continue
		}
		path := filepath.Join("/dev", entry.Name())
		var candidate unix.Stat_t
		if unix.Stat(path, &candidate) != nil || candidate.Mode&unix.S_IFMT != unix.S_IFCHR || candidate.Dev != original.Dev || candidate.Ino != original.Ino || candidate.Rdev != original.Rdev {
			continue
		}
		fd, err := unix.Open(path, mode|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOCTTY, 0)
		if err != nil {
			continue
		}
		var opened unix.Stat_t
		if unix.Fstat(fd, &opened) == nil && opened.Mode&unix.S_IFMT == unix.S_IFCHR && opened.Dev == original.Dev && opened.Ino == original.Ino && opened.Rdev == original.Rdev {
			return fd, nil
		}
		_ = unix.Close(fd)
	}
	return -1, errUnavailable
}
