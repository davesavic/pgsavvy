package machineio

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// XNU's write(2) records FWASWRITTEN (sys/fcntl.h) in F_GETFL after delivery.
// Ignore only this kernel activity bit, never any caller-configured flag.
func testConfiguredFlags(flags int) int { return flags &^ 0x00010000 }

func TestMachineIO_BlockingSocketOutput(t *testing.T) {
	left, right := testSocketPair(t, unix.SOCK_STREAM, false)
	before := testFlags(t, left)
	output, err := OpenOutput(context.Background(), left)
	if err != nil {
		t.Fatal("blocking output socket unavailable")
	}
	t.Cleanup(func() { testCleanupOwned(t, output, right, left) })
	if after := testFlags(t, left); after != before {
		t.Fatalf("output acquisition changed borrowed socket flags: %#x -> %#x", before, after)
	}
	input, err := OpenInput(context.Background(), left)
	if err != nil {
		t.Fatal("blocking socket input must remain supported")
	}
	var got [2]byte
	done := make(chan struct{})
	go func() { _, _ = input.Read(got[:]); close(done) }()
	t.Cleanup(func() { testCleanupOwned(t, input, right, left); testJoin(t, done, time.Second) })
	if after := testFlags(t, left); after != before {
		t.Fatalf("input acquisition changed borrowed socket flags: %#x -> %#x", before, after)
	}
	_ = input.Close()
	testJoin(t, done, time.Second)
	if after := testFlags(t, left); after != before {
		t.Fatalf("input close changed borrowed socket flags: %#x -> %#x", before, after)
	}
	if n, err := output.Write([]byte("ok")); err != nil || n != 2 {
		t.Fatal("blocking socket worker write failed")
	}
	if _, err := io.ReadFull(right, got[:]); err != nil || string(got[:]) != "ok" {
		t.Fatal("blocking socket worker changed bytes")
	}
	_ = output.Close()
	// A direct write sets Darwin's FWASWRITTEN bit. Exercise the borrowed
	// socket only after comparing flags against the untouched baseline.
	if _, err := left.Write([]byte("ok")); err != nil {
		t.Fatal("rejection or input close disabled borrowed socket")
	}
	if _, err := io.ReadFull(right, got[:]); err != nil || string(got[:]) != "ok" {
		t.Fatal("rejection or input close changed borrowed socket delivery")
	}
}

func TestMachineIO_KqueueDescriptorInventory(t *testing.T) {
	// Warm the inventory before comparing counts.
	before := testDescriptorCount(t)
	fd, err := unix.Kqueue()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	if got := testDescriptorCount(t); got != before+1 {
		t.Fatalf("kqueue descriptor was not counted: %d -> %d", before, got)
	}
}

func TestMachineIO_BlockingFIFO(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = read.Close() }()
	defer func() { _ = write.Close() }()
	for _, file := range []*os.File{read, write} {
		testControl(t, file, func(fd int) {
			if err := unix.SetNonblock(fd, false); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, pair := range []struct {
		file *os.File
		mode int
	}{{read, os.O_RDONLY}, {write, os.O_WRONLY}} {
		before := testFlags(t, pair.file)
		stream, err := acquire(context.Background(), pair.file, pair.mode)
		if err != nil || testFlags(t, pair.file) != before {
			t.Fatal("blocking FIFO unavailable or mutated")
		}
		testCleanupOwned(t, stream)
		if _, err := pair.file.Stat(); err != nil {
			t.Fatal("worker close closed borrowed FIFO")
		}
	}
}

func TestMachineIO_MetadataDescriptorRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata-only")
	if err := os.WriteFile(path, []byte("retained"), 0o600); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Open(path, unix.O_EVTONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(fd), "owned-metadata-only")
	t.Cleanup(func() { _ = file.Close() })
	if input, err := OpenInput(context.Background(), file); err == nil {
		_ = input.Close()
		t.Fatal("metadata-only input accepted")
	}
	if output, err := OpenOutput(context.Background(), file); err == nil {
		_ = output.Close()
		t.Fatal("metadata-only output accepted")
	}
	if _, err := file.Stat(); err != nil {
		t.Fatal("rejection closed the inherited metadata descriptor")
	}
}

func testPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	master, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	m := os.NewFile(uintptr(master), "owned-pty-master")
	t.Cleanup(func() { _ = m.Close() })
	for _, request := range []uint{unix.TIOCPTYGRANT, unix.TIOCPTYUNLK} {
		if err := unix.IoctlSetInt(master, request, 0); err != nil {
			t.Fatal(err)
		}
	}
	var name [128]byte
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(master), unix.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0])))
	runtime.KeepAlive(&name)
	if errno != 0 {
		t.Fatal(errno)
	}
	slave, err := unix.Open(unix.ByteSliceToString(name[:]), unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	s := os.NewFile(uintptr(slave), "owned-pty-slave")
	t.Cleanup(func() { _ = s.Close() })
	return m, s
}

func testOwnedFD(t *testing.T, stream io.Closer) int {
	t.Helper()
	switch stream := stream.(type) {
	case *pollStream:
		return stream.fd
	case *os.File:
		fd := -1
		testControl(t, stream, func(value int) { fd = value })
		return fd
	default:
		t.Fatal("unexpected Darwin stream type")
		return -1
	}
}

func testTermios(t *testing.T, file *os.File) *unix.Termios {
	t.Helper()
	var value *unix.Termios
	testControl(t, file, func(fd int) {
		var err error
		value, err = unix.IoctlGetTermios(fd, unix.TIOCGETA)
		if err != nil {
			t.Fatal(err)
		}
	})
	return value
}
