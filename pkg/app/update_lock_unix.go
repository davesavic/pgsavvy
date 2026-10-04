//go:build linux || darwin

package app

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func openUpdateLockFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR|unix.O_NOFOLLOW, 0)
}

func lockUpdateFile(f *os.File) error {
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EWOULDBLOCK) {
			return fmt.Errorf("update in progress (lock held at %q)", f.Name())
		}
		return err
	}
}
