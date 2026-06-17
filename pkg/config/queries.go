package config

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// queriesFile is the on-disk wrapper for queries.yml: a mapping with a
// top-level `queries:` key whose value is the (possibly empty or null)
// sequence of saved queries. This mirrors the connectionsFile precedent.
type queriesFile struct {
	Queries []models.SavedQuery `yaml:"queries"`
}

// LoadQueries reads the YAML queries file at path. A missing file returns
// ([]models.SavedQuery{}, nil) so first-run is not an error. The file must
// contain a top-level mapping with a `queries:` key; a parse failure is
// returned wrapped.
func LoadQueries(fs afero.Fs, path string) ([]models.SavedQuery, error) {
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []models.SavedQuery{}, nil
		}
		return nil, fmt.Errorf("config: read %q: %w", path, err)
	}

	var wrapper queriesFile
	if err := yaml.Unmarshal(data, &wrapper); err != nil {
		if errors.Is(err, io.EOF) {
			return []models.SavedQuery{}, nil
		}
		return nil, fmt.Errorf("config: parse %q: %w", path, err)
	}
	if wrapper.Queries == nil {
		return []models.SavedQuery{}, nil
	}
	return wrapper.Queries, nil
}
