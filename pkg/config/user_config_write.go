package config

import (
	"errors"

	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/utils"
)

// SaveUserConfig atomically writes cfg to the YAML file at path. It serialises
// *UserConfig directly via utils.AtomicWriteYAML with mode 0o600 — no wrapper
// struct is needed because config.yml IS the UserConfig root.
//
// NOTE: re-encoding via yaml.Marshal LOSES inline YAML comments present in the
// original file, and key ordering is lost on save. User-authored config.yml
// files should expect comments and ordering to disappear after
// SaveUserConfig. Unrecognised settings from newer pgsavvy versions are
// silently dropped because they have no home in the current UserConfig struct.
func SaveUserConfig(fs afero.Fs, path string, cfg *UserConfig) error {
	if cfg == nil {
		return errors.New("config: nil *UserConfig")
	}
	return utils.AtomicWriteYAML(fs, path, cfg, 0o600)
}
