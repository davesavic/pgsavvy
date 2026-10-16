package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"
	"golang.org/x/sys/windows"
)

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
