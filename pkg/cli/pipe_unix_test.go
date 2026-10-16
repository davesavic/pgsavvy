//go:build !windows

package cli

import "os"

func machineInputPipe() (*os.File, *os.File, error)  { return os.Pipe() }
func machineOutputPipe() (*os.File, *os.File, error) { return os.Pipe() }
