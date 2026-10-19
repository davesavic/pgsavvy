package config

import (
	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// UpdateConnectionWithCLISettings atomically edits a profile and its explicitly
// changed CLI policies. A nil expected value preserves that policy from disk;
// a nonnil value checks the original before applying newConn's selected value.
// Either conflict rejects the entire edit, including rename and ordinary fields.
func UpdateConnectionWithCLISettings(fs afero.Fs, path, oldName string, newConn models.Connection, expectedAccess *models.CLIAccess, expectedHistory *bool) error {
	return withConnectionsWriteLock(fs, path, func(path string) error {
		return updateConnectionLocked(fs, path, oldName, newConn, expectedAccess, expectedHistory)
	})
}
