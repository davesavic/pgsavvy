//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package query

import (
	"os"
	"syscall"
)

func safeHistoryFile(info os.FileInfo, private bool) bool {
	if info == nil || !info.Mode().IsRegular() {
		return false
	}
	if !private {
		return true
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1 && stat.Uid == uint32(os.Geteuid()) && info.Mode().Perm()&0o077 == 0
}

func safeHistoryDirectory(info os.FileInfo, private bool) bool {
	if !private {
		return true
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && info.Mode().Perm()&0o022 == 0
}

func safeHistoryAncestor(info os.FileInfo, private bool) bool {
	if !private {
		return true
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && (stat.Uid == 0 || stat.Uid == uint32(os.Geteuid())) &&
		(info.Mode().Perm()&0o022 == 0 || info.Mode()&os.ModeSticky != 0)
}
