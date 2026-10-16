//go:build linux || darwin

package machineio

import (
	"io"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// Bounded polling permits Close to join operations before releasing the fd.
// Direction locks cover the entire partial-I/O loop, including queued callers.
type pollStream struct {
	fd        int
	socket    bool
	mu        sync.Mutex
	readMu    sync.Mutex
	writeMu   sync.Mutex
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
		_, err := unix.Poll(fds, 10)
		if err == unix.EINTR {
			continue
		}
		if err != nil || fds[0].Revents&unix.POLLNVAL != 0 {
			return errUnavailable
		}
		// Darwin FIFO writer closure need not produce readiness. Retry the
		// nonblocking syscall after a timeout so it can observe EOF.
		return nil
	}
}

func (f *pollStream) Read(b []byte) (int, error) {
	if !f.begin() {
		return 0, os.ErrClosed
	}
	defer f.ops.Done()
	f.readMu.Lock()
	defer f.readMu.Unlock()
	if f.canceled() {
		return 0, os.ErrClosed
	}
	if len(b) == 0 {
		return 0, nil
	}
	for {
		if f.canceled() {
			return 0, os.ErrClosed
		}
		n, err := f.receive(b)
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
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
	if f.canceled() {
		return 0, os.ErrClosed
	}
	written := 0
	for written < len(b) {
		if f.canceled() {
			return written, os.ErrClosed
		}
		n, err := f.send(b[written:])
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
