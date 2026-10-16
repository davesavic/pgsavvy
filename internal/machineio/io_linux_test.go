package machineio

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestMachineIO_PipeFlags(t *testing.T) {
	for _, named := range []bool{false, true} {
		for _, nonblocking := range []bool{false, true} {
			t.Run(fmt.Sprintf("named=%t/nonblocking=%t", named, nonblocking), func(t *testing.T) {
				read, write, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				if named {
					_ = read.Close()
					_ = write.Close()
					path := filepath.Join(t.TempDir(), "fifo")
					if err := unix.Mkfifo(path, 0o600); err != nil {
						t.Fatal(err)
					}
					read, err = os.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK, 0)
					if err != nil {
						t.Fatal(err)
					}
					write, err = os.OpenFile(path, os.O_WRONLY|unix.O_NONBLOCK, 0)
					if err != nil {
						t.Fatal(err)
					}
				}
				t.Cleanup(func() { _ = read.Close(); _ = write.Close() })
				for _, file := range []*os.File{read, write} {
					testControl(t, file, func(fd int) {
						if err := unix.SetNonblock(fd, nonblocking); err != nil {
							t.Fatal(err)
						}
					})
				}
				rflags, wflags := testFlags(t, read), testFlags(t, write)
				check := func() {
					t.Helper()
					if testFlags(t, read) != rflags || testFlags(t, write) != wflags {
						t.Fatal("caller FIFO flags changed")
					}
				}
				input, err := OpenInput(context.Background(), read)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { testCleanupOwned(t, input, write) })
				output, err := OpenOutput(context.Background(), write)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { testCleanupOwned(t, output, read) })
				check()
				testOwnedDescriptor(t, input)
				testOwnedDescriptor(t, output)
				for _, pair := range []struct {
					source *os.File
					stream io.Closer
				}{{read, input}, {write, output}} {
					var before, after unix.Stat_t
					testControl(t, pair.source, func(fd int) {
						if err := unix.Fstat(fd, &before); err != nil {
							t.Fatal(err)
						}
					})
					if err := unix.Fstat(testOwnedFD(t, pair.stream), &after); err != nil || before.Dev != after.Dev || before.Ino != after.Ino || before.Mode&unix.S_IFMT != after.Mode&unix.S_IFMT {
						t.Fatal("FIFO reopen changed identity")
					}
				}
				readDone := make(chan struct{})
				go func() { _, _ = input.Read(make([]byte, 1)); close(readDone) }()
				t.Cleanup(func() { testCleanupOwned(t, input, write); testJoin(t, readDone, time.Second) })
				select {
				case <-readDone:
					t.Fatal("read was not blocked")
				case <-time.After(10 * time.Millisecond):
				}
				check()
				closed := make(chan struct{})
				go func() { _ = input.Close(); close(closed) }()
				testJoin(t, closed, time.Second)
				testJoin(t, readDone, time.Second)
				check()
				writeDone := make(chan struct{})
				var n int
				var writeErr error
				data := bytes.Repeat([]byte{'x'}, 4<<20)
				go func() { n, writeErr = output.Write(data); close(writeDone) }()
				t.Cleanup(func() { testCleanupOwned(t, output, read); testJoin(t, writeDone, 2*time.Second) })
				select {
				case <-writeDone:
					t.Fatal("write was not blocked")
				case <-time.After(10 * time.Millisecond):
				}
				check()
				closed = make(chan struct{})
				go func() { _ = output.Close(); close(closed) }()
				testJoin(t, closed, 2*time.Second)
				testJoin(t, writeDone, 2*time.Second)
				if n <= 0 || n >= len(data) || writeErr == nil {
					t.Fatal("lost partial FIFO write")
				}
				check()
			})
		}
	}
}

func TestMachineIO_PipeRejection(t *testing.T) {
	t.Run("identity", func(t *testing.T) {
		read, write, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = read.Close() }()
		defer func() { _ = write.Close() }()
		before := testFlags(t, read)
		entries, err := os.ReadDir("/dev/fd")
		if err != nil {
			t.Fatal(err)
		}
		testControl(t, read, func(fd int) {
			var original unix.Stat_t
			if err := unix.Fstat(fd, &original); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"inode", "device", "type"} {
				bad := original
				mode := os.O_RDONLY
				switch field {
				case "inode":
					bad.Ino++
				case "device":
					bad.Dev++
				case "type":
					bad.Mode = unix.S_IFREG
				}
				owned, err := reopenLinuxStream(fd, &bad, mode)
				if owned >= 0 {
					_ = unix.Close(owned)
				}
				if err == nil {
					t.Fatalf("mismatched %s accepted", field)
				}
			}
		})
		after, err := os.ReadDir("/dev/fd")
		if err != nil || len(entries) != len(after) || testFlags(t, read) != before {
			t.Fatal("failed identity validation leaked or mutated source")
		}
	})
	t.Run("packet", func(t *testing.T) {
		read, write, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = read.Close() }()
		defer func() { _ = write.Close() }()
		testControl(t, write, func(fd int) {
			flags := testFlags(t, write)
			if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFL, flags|unix.O_DIRECT); err != nil {
				t.Fatal(err)
			}
		})
		before := testFlags(t, write)
		if output, err := OpenOutput(context.Background(), write); output != nil || err == nil {
			if output != nil {
				_ = output.Close()
			}
			t.Fatal("O_DIRECT FIFO accepted")
		}
		if testFlags(t, write) != before {
			t.Fatal("O_DIRECT rejection changed flags")
		}
	})
	t.Run("reopen-denied", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses FIFO access permission checks")
		}
		path := filepath.Join(t.TempDir(), "denied-fifo")
		if err := unix.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
		read, err := os.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = read.Close() }()
		write, err := os.OpenFile(path, os.O_WRONLY|unix.O_NONBLOCK, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = write.Close() }()
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
		before := testFlags(t, write)
		if output, err := OpenOutput(context.Background(), write); output != nil || err == nil {
			if output != nil {
				_ = output.Close()
			}
			t.Fatal("unreopenable FIFO accepted")
		}
		if testFlags(t, write) != before {
			t.Fatal("failed reopen changed flags")
		}
	})
	t.Run("missing-reader", func(t *testing.T) {
		read, write, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = write.Close() }()
		_ = read.Close()
		before := testFlags(t, write)
		output, err := OpenOutput(context.Background(), write)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = output.Close() }()
		if _, err := output.Write([]byte("broken")); err == nil {
			t.Fatal("broken pipe accepted bytes")
		}
		if testFlags(t, write) != before {
			t.Fatal("broken-pipe write changed flags")
		}
	})
}

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
	if poll, ok := stream.(*pollStream); ok {
		return poll.fd
	}
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
