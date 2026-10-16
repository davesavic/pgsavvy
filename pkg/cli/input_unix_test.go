//go:build linux || darwin

package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/afero"
)

func TestMachineInput_UnixNonregular(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{fifo, "/dev/null"} {
		start := time.Now()
		if _, err := ReadSQL(context.Background(), path, nil); err == nil || safeError(err).Code != InvalidInput || time.Since(start) > time.Second {
			t.Fatal("nonregular SQL input was not rejected promptly")
		}
		if _, err := (machineProfileFS{afero.NewOsFs()}).Open(path); !errors.Is(err, os.ErrInvalid) || time.Since(start) > time.Second {
			t.Fatal("nonregular profile was not rejected promptly")
		}
	}
}

func TestMachineInput_UnixDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses Unix file permissions")
	}
	path := filepath.Join(t.TempDir(), "denied")
	if err := os.WriteFile(path, []byte("SELECT 1"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSQL(context.Background(), path, nil); err == nil || safeError(err).Code != ResourceFailure {
		t.Fatal("denied SQL open classification changed")
	}
	if _, err := (machineProfileFS{afero.NewOsFs()}).Open(path); !errors.Is(err, os.ErrPermission) {
		t.Fatal("profile adapter lost permission error")
	}
}
