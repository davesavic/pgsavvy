package cli

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"
	"golang.org/x/sys/windows"
)

func machineInputPipe() (*os.File, *os.File, error)  { return machineTestPipe(true) }
func machineOutputPipe() (*os.File, *os.File, error) { return machineTestPipe(false) }

func machineTestPipe(input bool) (*os.File, *os.File, error) {
	name, err := windows.UTF16PtrFromString(fmt.Sprintf(`\\.\pipe\pgsavvy-cli-test-%d-%s`, os.Getpid(), rand.Text()))
	if err != nil {
		return nil, nil, err
	}
	access, peerAccess := uint32(windows.PIPE_ACCESS_INBOUND), uint32(windows.GENERIC_WRITE)
	if !input {
		// Go's NewFile queries completion modes before enabling deadlines. That
		// query needs read attributes, which an outbound-only handle lacks.
		access, peerAccess = windows.PIPE_ACCESS_DUPLEX, windows.GENERIC_READ
	}
	serverHandle, err := windows.CreateNamedPipe(name, access|windows.FILE_FLAG_OVERLAPPED|windows.FILE_FLAG_FIRST_PIPE_INSTANCE,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS, 1, 4096, 4096, 0, nil)
	if err != nil {
		return nil, nil, err
	}
	// Connect the fresh synchronous peer before wrapping the overlapped server;
	// this one-shot fixture has no pending native accept to cancel or join.
	peerHandle, err := windows.CreateFile(name, peerAccess, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		_ = windows.CloseHandle(serverHandle)
		return nil, nil, err
	}
	server := os.NewFile(uintptr(serverHandle), "owned-cli-test-overlapped")
	peer := os.NewFile(uintptr(peerHandle), "owned-cli-test-peer")
	if server == nil || peer == nil {
		if server == nil {
			_ = windows.CloseHandle(serverHandle)
		} else {
			_ = server.Close()
		}
		if peer == nil {
			_ = windows.CloseHandle(peerHandle)
		} else {
			_ = peer.Close()
		}
		return nil, nil, os.ErrInvalid
	}
	if err := server.SetDeadline(time.Time{}); err != nil {
		_ = peer.Close()
		_ = server.Close()
		return nil, nil, err
	}
	if input {
		return server, peer, nil
	}
	return peer, server, nil
}

func TestMachineCLI_WindowsPipeDeadlineCapability(t *testing.T) {
	for _, input := range []bool{true, false} {
		read, write, err := machineTestPipe(input)
		if err != nil {
			t.Fatalf("pipe construction (input=%t): %v", input, err)
		}
		t.Cleanup(func() { _ = read.Close(); _ = write.Close() })
		server := write
		if input {
			server = read
		}
		if err := server.SetDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatalf("pipe deadline (input=%t): %v", input, err)
		}
		if err := server.SetDeadline(time.Time{}); err != nil {
			t.Fatalf("reset pipe deadline (input=%t): %v", input, err)
		}
	}
}

func TestMachineInput_WindowsPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SQL λ with spaces.sql")
	if err := os.WriteFile(path, []byte("SELECT 'λ';\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{path, `\\?\` + path} {
		if sql, err := ReadSQL(context.Background(), name, nil); err != nil || sql != "SELECT 'λ';\r\n" {
			t.Fatal("native regular-file path or SQL bytes changed")
		}
		file, err := (machineProfileFS{afero.NewOsFs()}).Open(name)
		if err != nil {
			t.Fatal("native profile path rejected")
		}
		_ = file.Close()
	}
	pipe := fmt.Sprintf(`\\.\pipe\pgsavvy-input-%d-%d`, os.Getpid(), time.Now().UnixNano())
	pipeName, err := windows.UTF16PtrFromString(pipe)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateNamedPipe(pipeName, windows.PIPE_ACCESS_DUPLEX, windows.PIPE_TYPE_BYTE|windows.PIPE_WAIT, 1, 1024, 1024, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = windows.CloseHandle(handle) })
	for _, name := range []string{"NUL", "nul", "CON", "CONIN$", "CONOUT$", "AUX", "PRN", "COM1", "LPT1", "COM¹", pipe, pipe + "-absent", `//./pipe/absent`, `\\?\pipe\absent`, `\??\pipe\absent`, `\\localhost\pipe\absent`, `\\?\UNC\localhost\pipe\absent`, `\\.\PhysicalDrive0`} {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			if _, err := ReadSQL(context.Background(), name, nil); err == nil || safeError(err).Code != InvalidInput || time.Since(start) > time.Second {
				t.Fatal("device/pipe input not rejected promptly with invalid input")
			}
			if _, err := (machineProfileFS{afero.NewOsFs()}).Open(name); !errors.Is(err, os.ErrInvalid) || time.Since(start) > time.Second {
				t.Fatal("device/pipe profile not rejected promptly")
			}
		})
	}
	wide, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	locked, err := windows.CreateFile(wide, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = windows.CloseHandle(locked) }()
	if _, err := ReadSQL(context.Background(), path, nil); err == nil || safeError(err).Code != ResourceFailure {
		t.Fatal("Windows sharing failure classification changed")
	}
	if _, err := (machineProfileFS{afero.NewOsFs()}).Open(path); !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatal("profile adapter lost Windows sharing error")
	}
}
