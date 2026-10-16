//go:build linux || darwin

package session

import (
	"io"
	"os"
	"syscall"
)

func readMachinePgpassFile(path string) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, &MachineCredentialError{Kind: MachineCredentialUnavailable}
	}
	defer func() { _ = file.Close() }()
	return readMachinePgpassHandle(file)
}

func readMachinePgpassHandle(file *os.File) ([]byte, error) {
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, &MachineCredentialError{Kind: MachineCredentialUnavailable}
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, &MachineCredentialError{Kind: MachineCredentialUnavailable}
	}
	return data, nil
}
