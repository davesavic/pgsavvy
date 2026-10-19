//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package query

import "os"

// Unix mode privacy is not a Windows ACL guarantee. Keep TUI opening portable;
// this helper does not activate the Linux-only machine CLI on other platforms.
func safeHistoryFile(info os.FileInfo, _ bool) bool {
	return info != nil && info.Mode().IsRegular()
}

func safeHistoryDirectory(_ os.FileInfo, _ bool) bool { return true }

func safeHistoryAncestor(_ os.FileInfo, _ bool) bool { return true }
