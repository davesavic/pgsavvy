package env

import (
	"os"
	"path/filepath"

	"github.com/adrg/xdg"

	"github.com/davesavic/pgsavvy/pkg/constants"
)

// GetConfigDir returns the per-user config directory for dbsavvy
// ($XDG_CONFIG_HOME/dbsavvy).
func GetConfigDir() string {
	return filepath.Join(xdg.ConfigHome, constants.XDGAppDir)
}

// GetStateDir returns the per-user state directory for dbsavvy
// ($XDG_STATE_HOME/dbsavvy).
func GetStateDir() string {
	return filepath.Join(xdg.StateHome, constants.XDGAppDir)
}

// GetCacheDir returns the per-user cache directory for dbsavvy
// ($XDG_CACHE_HOME/dbsavvy).
func GetCacheDir() string {
	return filepath.Join(xdg.CacheHome, constants.XDGAppDir)
}

// Getenv returns os.Getenv(key) when non-empty, else fallback.
func Getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
