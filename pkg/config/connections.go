package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// LoadConnections reads a YAML file containing a flat list of connection
// profiles. A missing file is treated as "no connections" and returns an
// empty slice with a nil error; malformed YAML or other read errors are
// returned wrapped.
func LoadConnections(fs afero.Fs, path string) ([]models.Connection, error) {
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []models.Connection{}, nil
		}
		return nil, fmt.Errorf("config: read %q: %w", path, err)
	}
	var conns []models.Connection
	if err := yaml.Unmarshal(data, &conns); err != nil {
		return nil, fmt.Errorf("config: parse %q: %w", path, err)
	}
	if conns == nil {
		conns = []models.Connection{}
	}
	return conns, nil
}
