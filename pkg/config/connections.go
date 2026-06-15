package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// warnWriter is the destination for non-fatal load-time warnings. Overridable
// in tests; defaults to os.Stderr so a single WARN line surfaces alongside
// other startup diagnostics.
var warnWriter io.Writer = os.Stderr

// connectionsFile is the on-disk wrapper enforced by D6: connections.yml must
// be a mapping with a top-level `connections:` key whose value is the profile
// sequence. The legacy flat-sequence form is rejected at load time.
type connectionsFile struct {
	Connections []models.Connection `yaml:"connections"`
}

const legacyMigrationSnippet = `connections:
  - name: dev
    driver: postgres
    dsn: postgres://localhost/dev`

// LoadConnections reads a YAML connections file. The file MUST contain a
// top-level mapping with a `connections:` key whose value is a (possibly
// empty or null) sequence of profiles. A missing file returns
// ([]models.Connection{}, nil); the legacy flat-list form is rejected with a
// typed error containing a paste-ready migration snippet (see DESIGN.md
// §11.2). Unknown keys at any level fail the load, naming the offending key.
//
// When the file carries any inline `password:` field and is group/world
// readable, a single WARN line is written to warnWriter; the load still
// succeeds.
func LoadConnections(fs afero.Fs, path string) ([]models.Connection, error) {
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []models.Connection{}, nil
		}
		return nil, fmt.Errorf("config: read %q: %w", path, err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("config: parse %q: %w", path, err)
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 && doc.Content[0].Kind == yaml.SequenceNode {
		return nil, fmt.Errorf(
			"config: %q is in legacy flat format (top-level sequence); expected key 'connections:' — see DESIGN.md §11.2.\nConvert your file to wrapper form:\n%s",
			path, legacyMigrationSnippet,
		)
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var wrapper connectionsFile
	if err := dec.Decode(&wrapper); err != nil {
		if errors.Is(err, io.EOF) {
			return []models.Connection{}, nil
		}
		return nil, fmt.Errorf("config: parse %q: %w", path, err)
	}
	if wrapper.Connections == nil {
		return []models.Connection{}, nil
	}

	hasInlinePassword := false
	for i := range wrapper.Connections {
		if wrapper.Connections[i].Password != "" {
			hasInlinePassword = true
			break
		}
	}
	if hasInlinePassword {
		if info, statErr := fs.Stat(path); statErr == nil {
			if info.Mode().Perm()&0o077 != 0 {
				_, _ = fmt.Fprintf(warnWriter,
					"config: %s contains plaintext password and is group/world readable (mode %04o); chmod 600 %s\n",
					path, info.Mode().Perm(), path,
				)
			}
		}
	}

	return wrapper.Connections, nil
}
