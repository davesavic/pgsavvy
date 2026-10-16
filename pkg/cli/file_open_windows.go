package cli

import (
	"os"
	"path/filepath"
	"strings"
)

func machineFileOpenFlags() int { return os.O_RDONLY }

// Stat may open a Windows device or pipe, so screen those paths before Stat.
func validateMachineFilePath(name string) error {
	path := strings.ReplaceAll(name, "/", `\`)
	for _, prefix := range []string{`\\.\`, `\\?\`, `\??\`} {
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		path = path[len(prefix):]
		if len(path) >= 4 && strings.EqualFold(path[:4], `UNC\`) {
			path = `\\` + path[4:]
		} else if len(path) < 3 || path[1] != ':' || path[2] != '\\' || !isMachineDriveLetter(path[0]) {
			return os.ErrInvalid
		}
		break
	}
	volume := filepath.VolumeName(path)
	if strings.HasPrefix(volume, `\\`) && strings.EqualFold(filepath.Base(volume), "pipe") {
		return os.ErrInvalid
	}
	for _, part := range strings.Split(path[len(volume):], `\`) {
		if part == "" || part == "." || part == ".." {
			continue
		}
		base, _, _ := strings.Cut(part, ":")
		// IsLocal uses the OS's device-name rules, including version-dependent extensions.
		if !filepath.IsLocal(base) {
			return os.ErrInvalid
		}
	}
	return nil
}

func isMachineDriveLetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}
