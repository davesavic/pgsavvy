//go:build windows

package logs

import (
	"syscall"

	"github.com/spf13/afero"
)

// forceCloseFd best-effort closes the underlying file handle when the
// afero.File is backed by *os.File on Windows.
func forceCloseFd(f afero.File) {
	type fdGetter interface{ Fd() uintptr }
	g, ok := f.(fdGetter)
	if !ok {
		return
	}
	_ = syscall.Close(syscall.Handle(g.Fd()))
}
