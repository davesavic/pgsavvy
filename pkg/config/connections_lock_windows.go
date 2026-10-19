package config

import (
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

func validateConnectionsBasename(name string) error {
	// Win32 short names, trimmed names and alternate streams do not derive the
	// same sidecar as the ordinary name. Reject them even before first creation:
	// an alias could otherwise start identifying a file during another save.
	if strings.ContainsAny(name, "~:") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return errors.New("ambiguous connections filename")
	}
	return nil
}

func openConnectionsLock(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	// Deny deletion/replacement while any participant owns a lock handle.
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_ALWAYS,
		windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(h), path)
	info, err := file.Stat()
	if err != nil || !safeConnectionsFile(info) {
		_ = file.Close()
		return nil, errors.New("unsafe connections lock")
	}
	return file, nil
}

func safeConnectionsFile(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}

func validateConnectionsLinks(path string) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return err
	}
	if info.NumberOfLinks != 1 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("unsafe connections target")
	}
	return nil
}

func tryConnectionsLock(file *os.File) (busy bool, err error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return false, err
	}
	if info.NumberOfLinks != 1 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return false, errors.New("unsafe connections lock")
	}
	overlapped := windows.Overlapped{Offset: 1 << 31}
	err = windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return true, nil
	}
	return false, err
}
