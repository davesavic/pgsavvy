//go:build !windows

package logs

import (
	"syscall"

	"github.com/spf13/afero"
)

// forceCloseFd best-effort closes the underlying file descriptor when the
// afero.File is backed by *os.File. No-op for non-os filesystems (e.g.
// MemMapFs in tests).
func forceCloseFd(f afero.File) {
	type fdGetter interface{ Fd() uintptr }
	g, ok := f.(fdGetter)
	if !ok {
		return
	}
	_ = syscall.Close(int(g.Fd()))
}
