//go:build linux || darwin

package machineio

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestMain(m *testing.M) {
	if os.Getenv("PGSAVVY_TEST_IO_CLOSED_STANDARDS") == "1" {
		os.Exit(testProcessClosedStandards())
	}
	if os.Getenv("PGSAVVY_TEST_IO_DELAYED_READY") == "1" {
		gate := os.NewFile(4, "test-worker-started")
		_, _ = gate.Write([]byte("1"))
		_ = gate.Close()
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	if handled, exit := RunHelper(); handled {
		os.Exit(exit)
	}
	os.Exit(m.Run())
}

func testIsolatedStream(t *testing.T, file *os.File, mode int) *processStream {
	t.Helper()
	fd := -1
	testControl(t, file, func(source int) {
		var err error
		fd, err = unix.FcntlInt(uintptr(source), unix.F_DUPFD_CLOEXEC, 3)
		if err != nil {
			t.Fatal(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := openProcessStream(ctx, fd, mode)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testCleanupOwned(t, stream) })
	if group, err := unix.Getpgid(stream.command.Process.Pid); err != nil || group != stream.command.Process.Pid {
		t.Fatal("worker shares the owner's foreground signal group")
	}
	return stream
}

func testBlockingPipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = read.Close(); _ = write.Close() })
	for _, file := range []*os.File{read, write} {
		testControl(t, file, func(fd int) {
			if err := unix.SetNonblock(fd, false); err != nil {
				t.Fatal(err)
			}
		})
	}
	return read, write
}

func TestMachineIO_ProcessOwnership(t *testing.T) {
	read, write := testBlockingPipe(t)
	before := testFlags(t, read)
	stream := testIsolatedStream(t, read, os.O_RDONLY)
	if testFlags(t, read) != before {
		t.Fatal("acquisition changed borrowed flags")
	}
	// Acquisition and idle workers must not prefetch from inherited input.
	if _, err := write.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	var first [1]byte
	if _, err := io.ReadFull(read, first[:]); err != nil || first[0] != 'a' {
		t.Fatal("worker consumed input before a request")
	}
	if err := read.Close(); err != nil {
		t.Fatal(err)
	}
	var rest [2]byte
	if _, err := io.ReadFull(stream, rest[:]); err != nil || string(rest[:]) != "bc" {
		t.Fatal("closing borrowed input invalidated owned stream")
	}
	_ = write.Close()
	for range 2 {
		if n, err := stream.Read(rest[:]); n != 0 || err != io.EOF {
			t.Fatalf("final writer EOF: %d %v", n, err)
		}
	}
}

func TestMachineIO_ProcessOutputAcknowledged(t *testing.T) {
	read, write := testBlockingPipe(t)
	before := testFlags(t, write)
	stream := testIsolatedStream(t, write, os.O_WRONLY)
	payload := bytes.Repeat([]byte("acknowledged\n"), helperChunk/3)
	got := make([]byte, len(payload))
	done := make(chan struct{})
	go func() { _, _ = io.ReadFull(read, got); close(done) }()
	if n, err := stream.Write(payload); err != nil || n != len(payload) {
		t.Fatalf("chunked write: %d %v", n, err)
	}
	testJoin(t, done, time.Second)
	if !bytes.Equal(got, payload) || testConfiguredFlags(testFlags(t, write)) != testConfiguredFlags(before) {
		t.Fatal("output changed bytes or borrowed flags")
	}
	_ = stream.Close()
	if _, err := write.Write([]byte("ok")); err != nil {
		t.Fatal("worker close invalidated borrowed output")
	}
	var tail [2]byte
	if _, err := io.ReadFull(read, tail[:]); err != nil || string(tail[:]) != "ok" {
		t.Fatal("borrowed output delivery changed")
	}
	_ = read.Close()
	broken := testIsolatedStream(t, write, os.O_WRONLY)
	if n, err := broken.Write([]byte("undeliverable")); n != 0 || err == nil {
		t.Fatal("IPC enqueue counted as destination delivery")
	}
}

func TestMachineIO_ProcessCancellation(t *testing.T) {
	for range 20 {
		for _, mode := range []int{os.O_RDONLY, os.O_WRONLY} {
			read, write := testBlockingPipe(t)
			file := read
			if mode == os.O_WRONLY {
				file = write
			}
			before := testFlags(t, file)
			stream := testIsolatedStream(t, file, mode)
			done := make(chan struct{})
			started := make(chan struct{})
			go func() {
				close(started)
				if mode == os.O_RDONLY {
					_, _ = stream.Read(make([]byte, 1))
				} else {
					_, _ = stream.Write(make([]byte, 2<<20))
				}
				close(done)
			}()
			<-started
			select {
			case <-done:
				t.Fatal("operation did not observe destination backpressure")
			case <-time.After(20 * time.Millisecond):
			}
			closed := make(chan struct{})
			go func() {
				var group sync.WaitGroup
				for range 3 {
					group.Go(func() { _ = stream.Close() })
				}
				group.Wait()
				close(closed)
			}()
			testJoin(t, closed, time.Second)
			testJoin(t, done, time.Second)
			select {
			case <-stream.waited:
			default:
				t.Fatal("close returned before worker was reaped")
			}
			if testConfiguredFlags(testFlags(t, file)) != testConfiguredFlags(before) {
				t.Fatal("cancellation changed borrowed flags")
			}
			_ = read.Close()
			_ = write.Close()
		}
	}
}

func TestMachineIO_ProcessIndependentInvocations(t *testing.T) {
	read, write := testBlockingPipe(t)
	first := testIsolatedStream(t, read, os.O_RDONLY)
	second := testIsolatedStream(t, read, os.O_RDONLY)
	_ = first.Close()
	if _, err := write.Write([]byte("independent")); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, len("independent"))
	if _, err := io.ReadFull(second, data); err != nil || string(data) != "independent" {
		t.Fatal("canceling another worker invalidated sibling input")
	}
}

func TestMachineIO_ProcessSocketCancellation(t *testing.T) {
	for range 20 {
		left, right := testSocketPair(t, unix.SOCK_STREAM, false)
		testControl(t, left, func(fd int) {
			if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_SNDBUF, 4096); err != nil {
				t.Fatal(err)
			}
		})
		before := testFlags(t, left)
		options := func() []int {
			var values []int
			testControl(t, left, func(fd int) {
				for _, option := range []int{unix.SO_SNDBUF, unix.SO_RCVBUF, unix.SO_KEEPALIVE} {
					value, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, option)
					if err != nil {
						t.Fatal(err)
					}
					values = append(values, value)
				}
			})
			return values
		}
		beforeOptions := options()
		stream := testIsolatedStream(t, left, os.O_WRONLY)
		done := make(chan struct{})
		var written int
		var writeErr error
		go func() { written, writeErr = stream.Write(make([]byte, 2<<20)); close(done) }()
		t.Cleanup(func() { testCleanupOwned(t, stream, right, left); testJoin(t, done, time.Second) })
		select {
		case <-done:
			t.Fatal("socket write did not establish backpressure")
		case <-time.After(20 * time.Millisecond):
		}
		closed := make(chan struct{})
		go func() { _ = stream.Close(); close(closed) }()
		testJoin(t, closed, 2*time.Second)
		testJoin(t, done, time.Second)
		if written != 0 || writeErr == nil {
			t.Fatal("unacknowledged blocked socket write reported delivery")
		}
		if testFlags(t, left) != before || !slices.Equal(options(), beforeOptions) {
			t.Fatal("worker cancellation changed borrowed socket flags/options")
		}
		drained := make(chan struct{})
		go func() { _, _ = io.Copy(io.Discard, right); close(drained) }()
		if _, err := left.Write([]byte("still usable")); err != nil {
			t.Fatal("worker cancellation shut down borrowed socket")
		}
		_ = left.Close()
		testJoin(t, drained, time.Second)
		_ = right.Close()
	}
}

func TestMachineIO_ProcessProtocolFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		n      uint32
		status uint32
		short  bool
	}{
		{"excess count", 2, helperOK, false},
		{"unknown status", 0, helperFailed + 1, false},
		{"zero progress", 0, helperOK, false},
		{"truncated header", 0, helperOK, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read, write := testBlockingPipe(t)
			stream := testIsolatedStream(t, read, os.O_RDONLY)
			// Replace only the private response channel, not the destination.
			_ = stream.responses.Close()
			responseRead, responseWrite, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			stream.responses = responseRead
			var reply [8]byte
			binary.BigEndian.PutUint32(reply[:4], tc.n)
			binary.BigEndian.PutUint32(reply[4:], tc.status)
			data := reply[:]
			if tc.short {
				data = data[:3]
			}
			if _, err := responseWrite.Write(data); err != nil {
				t.Fatal(err)
			}
			_ = responseWrite.Close()
			if n, err := stream.Read(make([]byte, 1)); n != 0 || err == nil {
				t.Fatal("invalid private protocol accepted")
			}
			if !stream.canceled() {
				t.Fatal("protocol failure left worker reusable")
			}
			if _, err := stream.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
				t.Fatal("retired stream accepted another operation")
			}
			_ = write.Close()
		})
	}
}

func TestMachineIO_ProcessAcquisitionLifetime(t *testing.T) {
	read, write := testBlockingPipe(t)
	ctx, cancel := context.WithCancel(context.Background())
	fd := -1
	testControl(t, read, func(source int) {
		var err error
		fd, err = unix.FcntlInt(uintptr(source), unix.F_DUPFD_CLOEXEC, 3)
		if err != nil {
			t.Fatal(err)
		}
	})
	stream, err := openProcessStream(ctx, fd, os.O_RDONLY)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { testCleanupOwned(t, stream) })
	cancel()
	if _, err := write.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	var data [2]byte
	if _, err := io.ReadFull(stream, data[:]); err != nil || string(data[:]) != "ok" {
		t.Fatal("acquisition context canceled returned stream")
	}
	fd = -1
	testControl(t, read, func(source int) {
		fd, err = unix.FcntlInt(uintptr(source), unix.F_DUPFD_CLOEXEC, 3)
	})
	if err != nil {
		t.Fatal(err)
	}
	if canceled, err := openProcessStream(ctx, fd, os.O_RDONLY); canceled != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("canceled acquisition started a worker")
	}
}

func TestMachineIO_ProcessStartupCancellation(t *testing.T) {
	read, _ := testBlockingPipe(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	gateRead, gateWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gateRead.Close(); _ = gateWrite.Close() }()
	command := exec.Command(executable)
	command.Env = []string{"PGSAVVY_TEST_IO_DELAYED_READY=1"}
	command.ExtraFiles = []*os.File{gateWrite}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	var stream *processStream
	var acquireErr error
	go func() {
		stream, acquireErr = startProcessStream(ctx, command, read, os.O_RDONLY)
		close(done)
	}()
	started := make(chan struct{})
	go func() { _, _ = io.ReadFull(gateRead, make([]byte, 1)); close(started) }()
	t.Cleanup(func() {
		cancel()
		_ = gateRead.Close()
		_ = gateWrite.Close()
		testJoin(t, done, 2*time.Second)
		testJoin(t, started, time.Second)
	})
	testJoin(t, started, 2*time.Second)
	cancel()
	testJoin(t, done, time.Second)
	if stream != nil || !errors.Is(acquireErr, context.Canceled) || command.ProcessState == nil {
		t.Fatalf("startup cancellation did not join/reap worker: stream=%t err=%v waited=%t", stream != nil, acquireErr, command.ProcessState != nil)
	}
}

func TestMachineIO_ProcessHelperPrivate(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"--help"}} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		command := exec.CommandContext(ctx, executable, args...)
		command.Env = []string{helperEnv + "=invalid"}
		output, err := command.CombinedOutput()
		cancel()
		if err == nil || len(output) != 0 {
			t.Fatal("invalid helper invocation reached application/test startup")
		}
	}
}

func testProcessClosedStandards() int {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return 1
	}
	defer func() { _ = unix.Close(fds[0]); _ = unix.Close(fds[1]) }()
	unix.CloseOnExec(fds[0])
	unix.CloseOnExec(fds[1])
	for fd := 0; fd < 3; fd++ {
		_ = unix.Close(fd)
	}
	fd, err := unix.FcntlInt(uintptr(fds[0]), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := openProcessStream(ctx, fd, os.O_RDONLY)
	if err != nil {
		return 1
	}
	defer func() { _ = stream.Close() }()
	// Go may initialize its own poller in a vacant slot. Our IPC must not
	// occupy a standard slot or be mistaken for inherited stdin/out/err.
	for _, file := range []*os.File{stream.requests, stream.responses} {
		raw, err := file.SyscallConn()
		if err != nil {
			return 1
		}
		valid := false
		if err := raw.Control(func(fd uintptr) {
			flags, err := unix.FcntlInt(fd, unix.F_GETFD, 0)
			valid = fd >= 3 && err == nil && flags&unix.FD_CLOEXEC != 0
		}); err != nil || !valid {
			return 1
		}
	}
	if _, err := unix.Write(fds[1], []byte("ok")); err != nil {
		return 1
	}
	var data [2]byte
	if _, err := io.ReadFull(stream, data[:]); err != nil || string(data[:]) != "ok" {
		return 1
	}
	return 0
}

func TestMachineIO_ProcessClosedStandardSlots(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable)
	command.Env = []string{"PGSAVVY_TEST_IO_CLOSED_STANDARDS=1"}
	if output, err := command.CombinedOutput(); err != nil || len(output) != 0 {
		t.Fatalf("worker IPC reused a closed standard slot: %v", err)
	}
}
