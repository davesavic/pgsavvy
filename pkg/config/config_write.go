package config

import (
	"errors"
	"fmt"
	iofs "io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"
)

// configFileName is the canonical filename for the user UserConfig YAML. The
// full path used by EnsureInitialConfig is configDir/configFileName.
const configFileName = "config.yml"

// EnsureInitialConfig writes a minimal initial config.yml into configDir if
// and only if no such file exists. It is a no-op on an existing file (M10b)
// and uses afero.OpenFile with O_CREATE|O_EXCL so a concurrent second caller
// observes EEXIST and treats it as success.
//
// On first creation:
//   - The parent directory is created with mode 0700 if it does not exist.
//   - The config file is written with mode 0600.
//   - The template body is the YAML form of config.GetDefaultConfig(), which
//     by construction never contains password, password_command, or dsn keys
//     (M10h).
//
// EnsureInitialConfig does NOT chmod the parent directory if it already
// exists (M10c) — if the user's XDG_CONFIG_HOME is group/world readable we
// leave it as-is.
func EnsureInitialConfig(fs afero.Fs, configDir string) error {
	path := filepath.Join(configDir, configFileName)

	// Fast-path: file already there. We still need to NOT-create the parent
	// dir / NOT-chmod it. Stat the parent to decide whether to MkdirAll.
	if exists, _ := afero.Exists(fs, path); exists {
		return nil
	}

	if _, err := fs.Stat(configDir); err != nil {
		if !errors.Is(err, iofs.ErrNotExist) {
			return fmt.Errorf("config: stat configDir %s: %w", configDir, err)
		}
		if err := fs.MkdirAll(configDir, 0o700); err != nil {
			return fmt.Errorf("config: mkdir %s: %w", configDir, err)
		}
	}

	body, err := initialConfigTemplate()
	if err != nil {
		return fmt.Errorf("config: marshal default: %w", err)
	}

	// O_CREATE|O_EXCL: atomic single-creator semantics. If a concurrent
	// caller wrote the file between the Exists check and now, EEXIST is
	// treated as success.
	f, err := fs.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil
		}
		return fmt.Errorf("config: create %s: %w", path, err)
	}
	if _, werr := f.Write(body); werr != nil {
		_ = f.Close()
		return fmt.Errorf("config: write %s: %w", path, werr)
	}
	if cerr := f.Close(); cerr != nil {
		return fmt.Errorf("config: close %s: %w", path, cerr)
	}
	return nil
}

// initialConfigTemplate returns the YAML body written by EnsureInitialConfig
// on first run. It is generated from GetDefaultConfig() so that the template
// can never drift from the runtime default (M10g). By construction the
// returned bytes contain no password / password_command / dsn fields.
func initialConfigTemplate() ([]byte, error) {
	return yaml.Marshal(GetDefaultConfig())
}
