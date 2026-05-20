//go:build linux || darwin

package logs

import "golang.org/x/sys/unix"

const platformNoFollow = unix.O_NOFOLLOW
