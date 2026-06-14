package env

import (
	"os"
	"path/filepath"
)

// GetDownloadDir returns the user's download directory using the
// fallback chain documented in AD-15:
//
//  1. $XDG_DOWNLOAD_DIR when set and non-empty.
//  2. $HOME/Downloads when the directory exists.
//  3. os.TempDir() as a last-ditch fallback.
//
// Never panics; always returns a non-empty path.
func GetDownloadDir() string {
	if d := os.Getenv("XDG_DOWNLOAD_DIR"); d != "" {
		return d
	}
	if home, err := os.UserHomeDir(); err == nil {
		downloads := filepath.Join(home, "Downloads")
		if info, err := os.Stat(downloads); err == nil && info.IsDir() {
			return downloads
		}
	}
	return os.TempDir()
}
