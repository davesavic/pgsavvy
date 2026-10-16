//go:build linux || darwin

package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestMachinePgpass_Privacy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private λ pgpass")
	for _, mode := range []os.FileMode{0o600, 0o400, 0o700, 0o640, 0o604, 0o666, 0o701} {
		if err := os.WriteFile(path, []byte("synthetic-password\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		data, err := readMachinePgpassFile(path)
		if mode&0o077 == 0 {
			if err != nil || string(data) != "synthetic-password\n" {
				t.Fatalf("private mode %o rejected", mode)
			}
		} else {
			var safe *MachineCredentialError
			if !errors.As(err, &safe) || safe.Kind != MachineCredentialUnavailable || data != nil {
				t.Fatalf("broad mode %o accepted or leaked bytes", mode)
			}
		}
		info, statErr := os.Stat(path)
		if statErr != nil || info.Mode().Perm() != mode {
			t.Fatal("pgpass reader changed file permissions")
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		unchanged, readErr := os.ReadFile(path)
		if readErr != nil || string(unchanged) != "synthetic-password\n" {
			t.Fatal("pgpass reader changed file contents")
		}
	}
	fifo := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Dir(fifo), fifo, "/dev/null", fifo + "-missing-SECRET"} {
		data, err := readMachinePgpassFile(path)
		var safe *MachineCredentialError
		if !errors.As(err, &safe) || safe.Kind != MachineCredentialUnavailable || data != nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatal("nonregular/missing pgpass classification or privacy changed")
		}
	}
}

func TestMachinePgpass_CheckedHandle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pass")
	if err := os.WriteFile(path, []byte("checked"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if err := os.Rename(path, path+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("unchecked"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if data, err := readMachinePgpassHandle(file); err != nil || string(data) != "checked" {
		t.Fatal("pathname replacement changed checked-handle bytes")
	}
	if _, err := readMachinePgpassFile(path); err == nil {
		t.Fatal("replacement privacy was not checked")
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if data, err := readMachinePgpassHandle(file); err == nil || data != nil {
		t.Fatal("closed handle returned credential bytes")
	}
}
