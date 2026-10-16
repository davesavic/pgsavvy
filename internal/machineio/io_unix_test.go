//go:build linux || darwin

package machineio

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func testControl(t *testing.T, file *os.File, callback func(int)) {
	t.Helper()
	raw, err := file.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Control(func(fd uintptr) { callback(int(fd)) }); err != nil {
		t.Fatal(err)
	}
}

func testFlags(t *testing.T, file *os.File) int {
	t.Helper()
	flags := -1
	testControl(t, file, func(fd int) {
		var err error
		flags, err = unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
		if err != nil {
			t.Fatal(err)
		}
	})
	return flags
}

func testOwnedDescriptor(t *testing.T, stream io.Closer) {
	t.Helper()
	fd := testOwnedFD(t, stream)
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if fd < 3 || err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatal("owned descriptor used a standard slot or was inheritable")
	}
}

func testJoin(t *testing.T, done <-chan struct{}, limit time.Duration) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatal("owned stream work failed to join")
	}
}

func testCleanupOwned(t *testing.T, stream io.Closer, peers ...io.Closer) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		// Release fixture peers before waiting for an owned Close that may be
		// regressing; EOF/broken-pipe can then release its outstanding operation.
		for _, peer := range peers {
			_ = peer.Close()
		}
		_ = stream.Close()
		close(done)
	}()
	testJoin(t, done, 2*time.Second)
}

type testCleanupCloser func() error

func (f testCleanupCloser) Close() error { return f() }

func TestMachineIO_CleanupPeerFirst(t *testing.T) {
	released, returned := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	t.Cleanup(func() { release(); testJoin(t, returned, time.Second) })
	peer := testCleanupCloser(func() error { release(); return nil })
	owned := testCleanupCloser(func() error { <-released; close(returned); return nil })
	testCleanupOwned(t, owned, peer)
	testJoin(t, returned, time.Second)
}

func testReadAll(t *testing.T, reader io.ReadCloser) []byte {
	t.Helper()
	done := make(chan struct{})
	var data []byte
	var readErr error
	go func() { data, readErr = io.ReadAll(reader); close(done) }()
	t.Cleanup(func() { testCleanupOwned(t, reader); testJoin(t, done, time.Second) })
	testJoin(t, done, time.Second)
	if readErr != nil {
		t.Fatal(readErr)
	}
	return data
}

func TestMachineIO_Ownership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared-offset")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	if _, err := file.WriteString("prefix:payload"); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(7, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	input, err := OpenInput(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}
	testOwnedDescriptor(t, input)
	if position, err := file.Seek(0, io.SeekCurrent); err != nil || position != 7 || testOwnedFD(t, input) < 3 {
		t.Fatal("acquisition changed inherited offset or descriptor slots")
	}
	if string(testReadAll(t, input)) != "payload" {
		t.Fatal("inherited offset/bytes changed")
	}
	_ = input.Close()
	if position, err := file.Seek(0, io.SeekCurrent); err != nil || position != int64(len("prefix:payload")) {
		t.Fatal("ordinary I/O no longer advances the shared inherited offset")
	}
	output, err := OpenOutput(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}
	testOwnedDescriptor(t, output)
	if _, err := output.Write([]byte("!")); err != nil {
		t.Fatal(err)
	}
	_ = output.Close()
	if _, err := file.WriteString("borrowed"); err != nil {
		t.Fatal("closing owned output closed the inherited handle")
	}
	appendFile, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = appendFile.Close() })
	if _, err := appendFile.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	appendOut, err := OpenOutput(context.Background(), appendFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appendOut.Write([]byte("append")); err != nil {
		t.Fatal(err)
	}
	_ = appendOut.Close()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "prefix:payload!borrowedappend" {
		t.Fatal("append behavior or bytes changed")
	}

	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = read.Close(); _ = write.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	pipeOut, err := OpenOutput(ctx, write)
	if err != nil {
		t.Fatal(err)
	}
	testOwnedDescriptor(t, pipeOut)
	cancel()
	if _, err := pipeOut.Write([]byte("raw\x00λ\n")); err != nil {
		t.Fatal("acquisition context consumed output lifetime")
	}
	_ = pipeOut.Close()
	if _, err := write.WriteString("borrowed"); err != nil {
		t.Fatal("inherited output was closed")
	}
	_ = write.Close()
	if got := string(testReadAll(t, read)); got != "raw\x00λ\nborrowed" {
		t.Fatal("pipe byte contract changed")
	}

	closed, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	for _, invalid := range []*os.File{nil, closed} {
		if _, err := OpenInput(context.Background(), invalid); err == nil || strings.Contains(err.Error(), path) {
			t.Fatal("invalid stream accepted or disclosed a native path")
		}
		if output, err := OpenOutput(context.Background(), invalid); err == nil || strings.Contains(err.Error(), path) {
			if output != nil {
				_ = output.Close()
			}
			t.Fatal("invalid output accepted or disclosed a native path")
		}
	}
	if _, err := OpenOutput(ctx, file); !errors.Is(err, context.Canceled) {
		t.Fatal("later acquisition failed to observe canceled startup context")
	}
	if _, err := file.Stat(); err != nil {
		t.Fatal("canceled acquisition changed a borrowed handle")
	}
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	if _, err := OpenInput(expired, file); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("expired acquisition did not preserve timeout classification")
	}
	if _, err := OpenOutput(expired, file); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("expired output did not preserve timeout classification")
	}
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	if input, err := OpenInput(context.Background(), directory); err == nil {
		_ = input.Close()
		t.Fatal("unsupported input descriptor accepted")
	}
	if output, err := OpenOutput(context.Background(), directory); err == nil {
		_ = output.Close()
		t.Fatal("unsupported output descriptor accepted")
	}
	if _, err := directory.Stat(); err != nil {
		t.Fatal("unsupported acquisition closed borrowed directory")
	}
	readOnly, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = readOnly.Close() })
	if output, err := OpenOutput(context.Background(), readOnly); err == nil {
		_ = output.Close()
		t.Fatal("read-only inherited handle acquired for writing")
	}
	var b [1]byte
	if _, err := readOnly.Read(b[:]); err != nil {
		t.Fatal("denied acquisition changed borrowed access")
	}
	writeOnly, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writeOnly.Close() })
	if input, err := OpenInput(context.Background(), writeOnly); err == nil {
		_ = input.Close()
		t.Fatal("write-only inherited handle acquired for reading")
	}
	if _, err := writeOnly.WriteString("retained"); err != nil {
		t.Fatal("denied acquisition closed a borrowed writer")
	}
}

func TestMachineIO_Cancellation(t *testing.T) {
	for range 20 {
		read, write, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		input, err := OpenInput(context.Background(), read)
		if err != nil {
			t.Fatal(err)
		}
		other, err := OpenInput(context.Background(), read)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { testCleanupOwned(t, other) })
		readStarted, readDone, closeDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
		go func() { close(readStarted); _, _ = input.Read(make([]byte, 1)); close(readDone) }()
		ctx, cancel := context.WithCancel(context.Background())
		go func() { <-ctx.Done(); _ = input.Close(); close(closeDone) }()
		borrowedRead, borrowedWrite := read, write
		t.Cleanup(func() {
			cancel()
			testCleanupOwned(t, input, borrowedWrite, borrowedRead)
			testJoin(t, closeDone, time.Second)
			testJoin(t, readDone, time.Second)
		})
		testJoin(t, readStarted, time.Second)
		select {
		case <-readDone:
			t.Fatal("fixture did not establish blocked input")
		case <-time.After(10 * time.Millisecond):
		}
		start := time.Now()
		cancel()
		cancel()
		testJoin(t, closeDone, time.Second)
		testJoin(t, readDone, time.Second)
		if time.Since(start) > time.Second {
			t.Fatal("input cancellation exceeded 1 second")
		}
		if _, err := write.Write([]byte{'x'}); err != nil {
			t.Fatal("cancellation closed an inherited handle")
		}
		var got [1]byte
		if _, err := io.ReadFull(other, got[:]); err != nil || got[0] != 'x' {
			t.Fatal("cancellation affected an independent invocation")
		}
		_ = other.Close()
		_ = read.Close()
		_ = write.Close()

		read, write, err = os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		output, err := OpenOutput(context.Background(), write)
		if err != nil {
			t.Fatal(err)
		}
		started, written := make(chan struct{}), make(chan struct{})
		go func() { close(started); _, _ = output.Write(bytes.Repeat([]byte{'x'}, 4<<20)); close(written) }()
		outputRead, outputWrite := read, write
		t.Cleanup(func() {
			testCleanupOwned(t, output, outputRead, outputWrite)
			testJoin(t, written, 2*time.Second)
		})
		testJoin(t, started, time.Second)
		select {
		case <-written:
			t.Fatal("fixture did not establish blocked output")
		case <-time.After(10 * time.Millisecond):
		}
		var closers sync.WaitGroup
		start = time.Now()
		for range 3 {
			closers.Go(func() { _ = output.Close() })
		}
		joined := make(chan struct{})
		go func() { closers.Wait(); close(joined) }()
		testJoin(t, joined, 2*time.Second)
		testJoin(t, written, 2*time.Second)
		if time.Since(start) > 2*time.Second {
			t.Fatal("blocked output close exceeded 2 seconds")
		}
		if _, err := write.Stat(); err != nil {
			t.Fatal("output cancellation closed inherited writer")
		}
		_ = read.Close()
		_ = write.Close()
	}
}

func TestMachineIO_Sockets(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	unix.CloseOnExec(fds[0])
	unix.CloseOnExec(fds[1])
	left, right := os.NewFile(uintptr(fds[0]), "owned-socket-left"), os.NewFile(uintptr(fds[1]), "owned-socket-right")
	t.Cleanup(func() { _ = left.Close(); _ = right.Close() })
	input, err := OpenInput(context.Background(), left)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testCleanupOwned(t, input, right, left) })
	if _, err := right.Write([]byte("raw")); err != nil {
		t.Fatal(err)
	}
	var got [3]byte
	if _, err := io.ReadFull(input, got[:]); err != nil || string(got[:]) != "raw" {
		t.Fatal("socket input bytes changed")
	}
	started, readDone := make(chan struct{}), make(chan struct{})
	go func() { close(started); _, _ = input.Read(make([]byte, 1)); close(readDone) }()
	t.Cleanup(func() { testCleanupOwned(t, input, right, left); testJoin(t, readDone, time.Second) })
	testJoin(t, started, time.Second)
	select {
	case <-readDone:
		t.Fatal("socket fixture did not establish blocked input")
	case <-time.After(10 * time.Millisecond):
	}
	inputClosed := make(chan struct{})
	start := time.Now()
	go func() { _ = input.Close(); close(inputClosed) }()
	testJoin(t, inputClosed, time.Second)
	testJoin(t, readDone, time.Second)
	if time.Since(start) > time.Second {
		t.Fatal("socket input did not join promptly")
	}
	output, err := OpenOutput(context.Background(), left)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testCleanupOwned(t, output, right, left) })
	if _, err := output.Write([]byte("out")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(right, got[:]); err != nil || string(got[:]) != "out" {
		t.Fatal("socket output bytes changed")
	}
	writing, written := make(chan struct{}), make(chan struct{})
	go func() { close(writing); _, _ = output.Write(bytes.Repeat([]byte{'x'}, 4<<20)); close(written) }()
	t.Cleanup(func() { testCleanupOwned(t, output, right, left); testJoin(t, written, 2*time.Second) })
	testJoin(t, writing, time.Second)
	select {
	case <-written:
		t.Fatal("socket fixture did not establish backpressure")
	case <-time.After(10 * time.Millisecond):
	}
	start = time.Now()
	outputClosed := make(chan struct{})
	go func() { _ = output.Close(); close(outputClosed) }()
	testJoin(t, outputClosed, 2*time.Second)
	testJoin(t, written, 2*time.Second)
	if time.Since(start) > 2*time.Second {
		t.Fatal("socket output did not join promptly")
	}
	if _, err := left.Stat(); err != nil {
		t.Fatal("closing owned socket closed inherited socket")
	}
}

func TestMachineIO_DarwinFIFOEOF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fifo")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	read, err := os.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = read.Close() })
	input, err := OpenInput(context.Background(), read)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testCleanupOwned(t, input) })
	writer, err := os.OpenFile(path, os.O_WRONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	_ = read.Close()
	consumed, eof := make(chan struct{}), make(chan struct{})
	var got [3]byte
	var dataErr, eofErr error
	go func() {
		_, dataErr = io.ReadFull(input, got[:])
		close(consumed)
		var b [1]byte
		_, eofErr = input.Read(b[:])
		close(eof)
	}()
	t.Cleanup(func() { testCleanupOwned(t, input, writer, read); testJoin(t, eof, time.Second) })
	if _, err := writer.Write([]byte("raw")); err != nil {
		t.Fatal(err)
	}
	testJoin(t, consumed, time.Second)
	if dataErr != nil || string(got[:]) != "raw" {
		t.Fatal("FIFO bytes changed")
	}
	_ = writer.Close()
	testJoin(t, eof, time.Second)
	if !errors.Is(eofErr, io.EOF) {
		t.Fatal("final-writer close did not produce EOF")
	}
}

func TestMachineIO_TerminalFlags(t *testing.T) {
	if os.Getenv("PGSAVVY_MACHINEIO_TTY_CHILD") == "1" {
		terminal := os.NewFile(3, "alternate-inherited-terminal")
		defer func() { _ = terminal.Close() }()
		before := testFlags(t, terminal)
		termios := testTermios(t, terminal)
		input, err := OpenInput(context.Background(), terminal)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = input.Close() }()
		output, err := OpenOutput(context.Background(), terminal)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = output.Close() }()
		if testFlags(t, terminal) != before || !reflect.DeepEqual(testTermios(t, terminal), termios) {
			t.Fatal("acquisition changed the parent terminal flags")
		}
		testControl(t, terminal, func(fd int) {
			if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFL, before&^unix.O_NONBLOCK); err != nil {
				t.Fatal(err)
			}
		})
		for _, owned := range []io.Closer{input, output} {
			flags, err := unix.FcntlInt(uintptr(testOwnedFD(t, owned)), unix.F_GETFL, 0)
			if err != nil || flags&unix.O_NONBLOCK == 0 {
				t.Fatal("terminal description was duplicated rather than independently reopened")
			}
		}
		testControl(t, terminal, func(fd int) {
			if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFL, before); err != nil {
				t.Fatal(err)
			}
		})
		var got [9]byte
		if _, err := io.ReadFull(input, got[:]); err != nil || string(got[:]) != "selected\n" {
			t.Fatal("adapter used the controlling terminal instead of the inherited one")
		}
		return
	}
	_, controlling := testPTY(t)
	master, inherited := testPTY(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMachineIO_TerminalFlags$")
	cmd.Env = append(os.Environ(), "PGSAVVY_MACHINEIO_TTY_CHILD=1")
	cmd.Stdin = controlling
	cmd.ExtraFiles = []*os.File{inherited}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	var diagnostic bytes.Buffer
	cmd.Stdout, cmd.Stderr = &diagnostic, &diagnostic
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := master.WriteString("selected\n"); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("isolated terminal child failed: %v: %s", err, diagnostic.String())
	}
}
