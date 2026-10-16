package machineio

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

func openStream(ctx context.Context, file *os.File, mode int) (io.ReadWriteCloser, error) {
	raw, err := file.SyscallConn()
	if err != nil {
		return nil, err
	}
	owned := -1
	regular := false
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
		source := int(fd)
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
		if setupErr == nil && !regular {
			setupErr = unix.SetNonblock(owned, true)
		}
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
	return &pollStream{fd: owned, closed: make(chan struct{})}, nil
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

// Darwin kqueue can miss a FIFO's final-writer-close event. Nonblocking syscalls
// with bounded poll waits preserve EOF and let Close join before releasing fd.
type pollStream struct {
	fd        int
	mu        sync.Mutex
	closing   bool
	closed    chan struct{}
	ops       sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
}

func (f *pollStream) begin() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closing {
		return false
	}
	f.ops.Add(1)
	return true
}

func (f *pollStream) canceled() bool {
	select {
	case <-f.closed:
		return true
	default:
		return false
	}
}

func (f *pollStream) wait(events int16) error {
	for {
		if f.canceled() {
			return os.ErrClosed
		}
		fds := []unix.PollFd{{Fd: int32(f.fd), Events: events}}
		n, err := unix.Poll(fds, 10)
		if err == unix.EINTR {
			continue
		}
		if err != nil || fds[0].Revents&unix.POLLNVAL != 0 {
			return errUnavailable
		}
		if n != 0 {
			return nil
		}
	}
}

func (f *pollStream) Read(b []byte) (int, error) {
	if !f.begin() {
		return 0, os.ErrClosed
	}
	defer f.ops.Done()
	if len(b) == 0 {
		return 0, nil
	}
	for {
		if f.canceled() {
			return 0, os.ErrClosed
		}
		n, err := unix.Read(f.fd, b)
		if n > 0 {
			return n, nil
		}
		if err == nil {
			return 0, io.EOF
		}
		if err == unix.EINTR {
			continue
		}
		if err != unix.EAGAIN && err != unix.EWOULDBLOCK {
			return 0, errUnavailable
		}
		if err := f.wait(unix.POLLIN); err != nil {
			return 0, err
		}
	}
}

func (f *pollStream) Write(b []byte) (int, error) {
	if !f.begin() {
		return 0, os.ErrClosed
	}
	defer f.ops.Done()
	written := 0
	for written < len(b) {
		if f.canceled() {
			return written, os.ErrClosed
		}
		n, err := unix.Write(f.fd, b[written:])
		if n > 0 {
			written += n
		}
		if err == nil && n > 0 || err == unix.EINTR {
			continue
		}
		if err != unix.EAGAIN && err != unix.EWOULDBLOCK {
			return written, errUnavailable
		}
		if err := f.wait(unix.POLLOUT); err != nil {
			return written, err
		}
	}
	return written, nil
}

func (f *pollStream) Close() error {
	f.closeOnce.Do(func() {
		f.mu.Lock()
		f.closing = true
		close(f.closed)
		f.mu.Unlock()
		f.ops.Wait()
		if unix.Close(f.fd) != nil {
			f.closeErr = errUnavailable
		}
	})
	return f.closeErr
}
