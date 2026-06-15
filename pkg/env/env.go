package env

import (
	"os"
	"path/filepath"

	"github.com/adrg/xdg"

	"github.com/davesavic/pgsavvy/pkg/constants"
)

// GetConfigDir returns the per-user config directory for pgsavvy
// ($XDG_CONFIG_HOME/pgsavvy).
func GetConfigDir() string {
	return filepath.Join(xdg.ConfigHome, constants.XDGAppDir)
}

// GetStateDir returns the per-user state directory for pgsavvy
// ($XDG_STATE_HOME/pgsavvy).
func GetStateDir() string {
	return filepath.Join(xdg.StateHome, constants.XDGAppDir)
}

// GetCacheDir returns the per-user cache directory for pgsavvy
// ($XDG_CACHE_HOME/pgsavvy).
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
