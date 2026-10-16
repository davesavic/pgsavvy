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
		if info.Mode&unix.S_IFMT == unix.S_IFCHR {
			// Reopening a PTY master allocates another PTY, not another description
			// of the inherited terminal. Unsupported/unverifiable devices fail closed.
			if _, ioctlErr := unix.IoctlGetInt(int(fd), unix.TIOCGPTN); ioctlErr != unix.ENOTTY {
				setupErr = errUnavailable
				return
			}
			source, setupErr = unix.Open("/proc/self/fd/"+strconv.Itoa(source), mode|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOCTTY, 0)
			if setupErr != nil {
				return
			}
			defer func() { _ = unix.Close(source) }()
		}
		owned, setupErr = unix.FcntlInt(uintptr(source), unix.F_DUPFD_CLOEXEC, 3)
		if setupErr != nil {
			return
		}
		if info.Mode&unix.S_IFMT == unix.S_IFIFO || info.Mode&unix.S_IFMT == unix.S_IFSOCK {
			setupErr = unix.SetNonblock(owned, true)
		}
	})
	if err != nil || setupErr != nil {
		if owned >= 0 {
			_ = unix.Close(owned)
		}
		return nil, errUnavailable
	}
	return os.NewFile(uintptr(owned), "pgsavvy-machine-stdio"), nil
}
