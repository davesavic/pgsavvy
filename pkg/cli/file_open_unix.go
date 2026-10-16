//go:build linux || darwin

package cli

import (
	"os"
	"syscall"
)

func validateMachineFilePath(string) error { return nil }

func machineFileOpenFlags() int { return os.O_RDONLY | syscall.O_NONBLOCK }
