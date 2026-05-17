package config

import (
	"fmt"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"
)

// LoadUserConfig builds a *UserConfig by starting from GetDefaultConfig and
// overlaying each YAML file in files (in order) onto the in-progress struct
// via yaml.Unmarshal. A nil or empty files slice returns the defaults
// unchanged. Any unreadable or malformed file aborts the load with a nil
// config and a wrapped error.
//
// Slice fields (Keybindings) are replaced atomically per overlay file; element-wise merge is not performed in v1.
func LoadUserConfig(fs afero.Fs, files []string) (*UserConfig, error) {
	cfg := GetDefaultConfig()
	for _, path := range files {
		data, err := afero.ReadFile(fs, path)
		if err != nil {
			return nil, fmt.Errorf("config: read %q: %w", path, err)
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("config: parse %q: %w", path, err)
		}
	}
	cfg.Sanitize()
	return cfg, nil
}
