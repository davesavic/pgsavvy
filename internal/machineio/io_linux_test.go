package machineio

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestMachineIO_PTYMasterRejected(t *testing.T) {
	master, _ := testPTY(t)
	input, err := OpenInput(context.Background(), master)
	if err == nil {
		original := -1
		testControl(t, master, func(fd int) { original, _ = unix.IoctlGetInt(fd, unix.TIOCGPTN) })
		reopened, _ := unix.IoctlGetInt(testOwnedFD(t, input), unix.TIOCGPTN)
		_ = input.Close()
		t.Fatalf("master was accepted: original PTY=%d reopened PTY=%d", original, reopened)
	}
	if output, err := OpenOutput(context.Background(), master); err == nil {
		_ = output.Close()
		t.Fatal("master output did not fail closed")
	}
	if _, err := master.Stat(); err != nil {
		t.Fatal("rejected master acquisition closed its borrowed handle")
	}
}

func TestMachineIO_MetadataDescriptorRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata-only")
	if err := os.WriteFile(path, []byte("retained"), 0o600); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
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
	if err := unix.IoctlSetPointerInt(master, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(master, unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := unix.Open(fmt.Sprintf("/dev/pts/%d", number), unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	s := os.NewFile(uintptr(slave), "owned-pty-slave")
	t.Cleanup(func() { _ = s.Close() })
	return m, s
}

func testOwnedFD(t *testing.T, stream io.Closer) int {
	t.Helper()
	file, ok := stream.(*os.File)
	if !ok {
		t.Fatal("unexpected Linux stream type")
	}
	fd := -1
	testControl(t, file, func(value int) { fd = value })
	return fd
}

func testTermios(t *testing.T, file *os.File) *unix.Termios {
	t.Helper()
	var value *unix.Termios
	testControl(t, file, func(fd int) {
		var err error
		value, err = unix.IoctlGetTermios(fd, unix.TCGETS)
		if err != nil {
			t.Fatal(err)
		}
	})
	return value
}
