package utils

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ExpandHome resolves a leading "~" to the user's home directory. A bare "~"
// or "~/..." is expanded; any other path is returned unchanged. A missing HOME
// (os.UserHomeDir error) yields a wrapped error rather than a panic.
func ExpandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory for ~ expansion: %w", err)
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}
