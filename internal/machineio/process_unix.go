//go:build linux || darwin

package machineio

import (
	"context"
	"encoding/binary"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	helperEnv    = "_PGSAVVY_MACHINE_IO"
	helperReady  = "pgsavvy-io-1\n"
	helperChunk  = 64 << 10
	helperOK     = 0
	helperEOF    = 1
	helperFailed = 2
)

// Blocking inherited pipes cannot acquire private O_NONBLOCK flags on Darwin.
// Isolate their syscalls in a killable child; acknowledge destination writes,
// not IPC enqueueing, so publication still observes backpressure and failures.
type processStream struct {
	mode      int
	requests  *os.File
	responses *os.File
	command   *exec.Cmd
	waited    chan struct{}
	closed    chan struct{}
	opMu      sync.Mutex
	eof       bool
	closeOnce sync.Once
}

// openProcessStream consumes fd, including on failure. The fd is an owned
// duplicate; neither the child nor the parent changes its shared status flags.
func openProcessStream(ctx context.Context, fd, mode int) (*processStream, error) {
	target := os.NewFile(uintptr(fd), "pgsavvy-machine-target")
	defer func() { _ = target.Close() }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, errUnavailable
	}
	direction := "read"
	if mode == os.O_WRONLY {
		direction = "write"
	}
	command := exec.Command(executable)
	command.Env = []string{helperEnv + "=" + direction}
	return startProcessStream(ctx, command, target, mode)
}

func startProcessStream(ctx context.Context, command *exec.Cmd, target *os.File, mode int) (*processStream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	requestRead, requestWrite, err := privatePipe()
	if err != nil {
		return nil, errUnavailable
	}
	defer func() { _ = requestRead.Close() }()
	responseRead, responseWrite, err := privatePipe()
	if err != nil {
		_ = requestWrite.Close()
		return nil, errUnavailable
	}
	defer func() { _ = responseWrite.Close() }()
	command.Stdin, command.Stdout = requestRead, responseWrite
	command.ExtraFiles = append([]*os.File{target}, command.ExtraFiles...)
	// Foreground Ctrl+C must cancel the owner, not kill its still-usable
	// publication worker before the owner can write the timeout document.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		_ = requestWrite.Close()
		_ = responseRead.Close()
		return nil, errUnavailable
	}
	_ = requestRead.Close()
	_ = responseWrite.Close()
	stream := &processStream{mode: mode, requests: requestWrite, responses: responseRead, command: command, waited: make(chan struct{}), closed: make(chan struct{})}
	go func() {
		_ = command.Wait()
		close(stream.waited)
	}()
	stopped := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = stream.Close(); close(stopped) })
	var ready [len(helperReady)]byte
	_, err = io.ReadFull(responseRead, ready[:])
	if !stop() {
		<-stopped
	}
	if ctx.Err() != nil || err != nil || string(ready[:]) != helperReady {
		_ = stream.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errUnavailable
	}
	return stream, nil
}

func privatePipe() (*os.File, *os.File, error) {
	read, write, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	files := []*os.File{read, write}
	for i, file := range files {
		raw, rawErr := file.SyscallConn()
		if rawErr != nil {
			err = rawErr
			break
		}
		owned := -1
		if rawErr = raw.Control(func(fd uintptr) {
			if fd < 3 {
				owned, err = unix.FcntlInt(fd, unix.F_DUPFD_CLOEXEC, 3)
			}
		}); rawErr != nil {
			err = rawErr
		}
		if owned >= 0 {
			_ = file.Close()
			files[i] = os.NewFile(uintptr(owned), "pgsavvy-machine-ipc")
		}
		if err != nil {
			break
		}
	}
	if err != nil {
		_ = files[0].Close()
		_ = files[1].Close()
		return nil, nil, err
	}
	return files[0], files[1], nil
}

func (s *processStream) canceled() bool {
	select {
	case <-s.closed:
		return true
	default:
		return false
	}
}

func (s *processStream) request(size int) error {
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(size))
	_, err := s.requests.Write(header[:])
	if err != nil {
		s.retire()
	}
	return err
}

func (s *processStream) response(limit int) (int, error) {
	var header [8]byte
	if _, err := io.ReadFull(s.responses, header[:]); err != nil {
		s.retire()
		return 0, errUnavailable
	}
	n, status := binary.BigEndian.Uint32(header[:4]), binary.BigEndian.Uint32(header[4:])
	if n > uint32(limit) || status > helperFailed || status == helperOK && n == 0 || status == helperEOF && s.mode != os.O_RDONLY {
		s.retire()
		return 0, errUnavailable
	}
	switch status {
	case helperEOF:
		return int(n), io.EOF
	case helperFailed:
		return int(n), errUnavailable
	default:
		return int(n), nil
	}
}

func (s *processStream) Read(p []byte) (int, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if s.canceled() {
		return 0, os.ErrClosed
	}
	if s.mode != os.O_RDONLY {
		return 0, errUnavailable
	}
	if s.eof {
		return 0, io.EOF
	}
	size := min(len(p), helperChunk)
	if size == 0 {
		return 0, nil
	}
	if err := s.request(size); err != nil {
		return 0, errUnavailable
	}
	n, resultErr := s.response(size)
	if n > 0 {
		if _, err := io.ReadFull(s.responses, p[:n]); err != nil {
			s.retire()
			return 0, errUnavailable
		}
	}
	if resultErr == io.EOF {
		s.eof = true
	} else if resultErr != nil {
		s.retire()
	}
	return n, resultErr
}

func (s *processStream) Write(p []byte) (int, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if s.mode != os.O_WRONLY {
		return 0, errUnavailable
	}
	written := 0
	for len(p) > 0 {
		if s.canceled() {
			return written, os.ErrClosed
		}
		size := min(len(p), helperChunk)
		if err := s.request(size); err != nil {
			return written, errUnavailable
		}
		if _, err := s.requests.Write(p[:size]); err != nil {
			s.retire()
			return written, errUnavailable
		}
		n, err := s.response(size)
		written += n
		if err != nil {
			s.retire()
			return written, errUnavailable
		}
		if n != size {
			s.retire()
			return written, io.ErrShortWrite
		}
		p = p[size:]
	}
	if s.canceled() {
		return written, os.ErrClosed
	}
	return written, nil
}

func (s *processStream) Close() error {
	s.retire()
	// IPC closure unblocks the active operation; acquiring its lock joins it.
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return nil
}

func (s *processStream) retire() {
	s.closeOnce.Do(func() {
		close(s.closed)
		_ = s.requests.Close()
		_ = s.responses.Close()
		_ = s.command.Process.Kill()
		<-s.waited
	})
}

// RunHelper handles only private same-executable stream workers. Call before
// normal routing, including in the package's test executable. No caller
// environment, CLI command, credential, or path is passed to the worker.
func RunHelper() (bool, int) {
	direction := os.Getenv(helperEnv)
	if direction == "" {
		return false, 0
	}
	if len(os.Args) != 1 || direction != "read" && direction != "write" {
		return true, 1
	}
	target := os.NewFile(3, "pgsavvy-machine-target")
	defer func() { _ = target.Close() }()
	var info unix.Stat_t
	flags, err := unix.FcntlInt(3, unix.F_GETFL, 0)
	if err != nil || unix.Fstat(3, &info) != nil {
		return true, 1
	}
	kind := info.Mode & unix.S_IFMT
	if kind != unix.S_IFIFO && kind != unix.S_IFSOCK || kind == unix.S_IFSOCK && checkStreamSocket(3) != nil {
		return true, 1
	}
	access := flags & unix.O_ACCMODE
	if direction == "read" && access == unix.O_WRONLY || direction == "write" && access == unix.O_RDONLY {
		return true, 1
	}
	unix.CloseOnExec(3)
	if _, err := io.WriteString(os.Stdout, helperReady); err != nil {
		return true, 1
	}
	var buffer [helperChunk]byte
	for {
		var request [4]byte
		if _, err := io.ReadFull(os.Stdin, request[:]); err != nil {
			return true, 0
		}
		size := binary.BigEndian.Uint32(request[:])
		if size == 0 || size > helperChunk {
			return true, 1
		}
		var n int
		var err error
		if direction == "write" {
			if _, err := io.ReadFull(os.Stdin, buffer[:size]); err != nil {
				return true, 1
			}
			if kind == unix.S_IFSOCK {
				// Send per-call flags, never shared socket options or write(2)'s
				// Darwin FWASWRITTEN bookkeeping on the borrowed description.
				for n < int(size) {
					var count int
					count, err = unix.SendmsgN(3, buffer[n:size], nil, nil, unix.MSG_NOSIGNAL)
					n += max(0, count)
					if err == unix.EINTR {
						continue
					}
					if err != nil || count == 0 {
						if err == nil {
							err = io.ErrNoProgress
						}
						break
					}
				}
			} else {
				n, err = target.Write(buffer[:size])
			}
		} else {
			n, err = target.Read(buffer[:size])
		}
		status := uint32(helperOK)
		if err == io.EOF {
			status = helperEOF
		} else if err != nil {
			status = helperFailed
		}
		var response [8]byte
		binary.BigEndian.PutUint32(response[:4], uint32(n))
		binary.BigEndian.PutUint32(response[4:], status)
		if _, err := os.Stdout.Write(response[:]); err != nil {
			return true, 1
		}
		if direction == "read" && n > 0 {
			if _, err := os.Stdout.Write(buffer[:n]); err != nil {
				return true, 1
			}
		}
		if status != helperOK {
			return true, 0
		}
	}
}
