package machineio

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

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
