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

// ErrExecutableCredentialsInsecureMode is returned when a configuration file
// writable by another local user contains a command that will be executed to
// resolve a credential. These commands are intentionally shell expressions,
// so their configuration must be trusted.
var ErrExecutableCredentialsInsecureMode = errors.New("config: executable credential command in group/world-writable connections file")

// connectionsFile is the on-disk wrapper enforced by D6: connections.yml must
// be a mapping with a top-level `connections:` key whose value is the profile
// sequence. The legacy flat-sequence form is rejected at load time.
type connectionsFile struct {
	// SchemaVersion is an informational marker only; no behavior is gated on
	// it. omitempty + a known struct field lets files WITHOUT it load cleanly
	// under dec.KnownFields(true), while files carrying it round-trip.
	SchemaVersion int                 `yaml:"schema_version,omitempty"`
	Connections   []models.Connection `yaml:"connections"`
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
// succeeds. A file writable by group or world is rejected when it contains an
// executable credential command, because another local user could otherwise
// replace that command before it runs.
func LoadConnections(fs afero.Fs, path string) ([]models.Connection, error) {
	return LoadConnectionsWithWarnings(fs, path, warnWriter)
}

// LoadConnectionsWithWarnings preserves strict loading with an invocation-local sink.
func LoadConnectionsWithWarnings(fs afero.Fs, path string, warnings io.Writer) ([]models.Connection, error) {
	if warnings == nil {
		warnings = io.Discard
	}
	file, err := fs.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []models.Connection{}, nil
		}
		return nil, fmt.Errorf("config: read %q: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("config: stat %q: %w", path, err)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("config: read %q: %w", path, err)
	}
	return decodeConnectionsWithWarnings(data, info, path, warnings)
}

func decodeConnections(data []byte, info os.FileInfo, path string) ([]models.Connection, error) {
	return decodeConnectionsWithWarnings(data, info, path, warnWriter)
}

func decodeConnectionsWithWarnings(data []byte, info os.FileInfo, path string, warnings io.Writer) ([]models.Connection, error) {
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

	if err := validateCLIHistoryNodes(&doc); err != nil {
		return nil, err
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
	for _, conn := range wrapper.Connections {
		if err := conn.CLIAccess.Validate(); err != nil {
			return nil, err
		}
	}
	if hasExecutableCredentialCommand(wrapper.Connections) {
		if info.Mode().Perm()&0o022 != 0 {
			return nil, fmt.Errorf("%w: chmod 600 %s", ErrExecutableCredentialsInsecureMode, path)
		}
	}

	hasInlinePassword := false
	for i := range wrapper.Connections {
		if wrapper.Connections[i].Password != "" {
			hasInlinePassword = true
			break
		}
	}
	if hasInlinePassword {
		if info.Mode().Perm()&0o077 != 0 {
			_, _ = fmt.Fprintf(warnings,
				"config: %s contains plaintext password and is group/world readable (mode %04o); chmod 600 %s\n",
				path, info.Mode().Perm(), path,
			)
		}
	}

	return wrapper.Connections, nil
}

// yaml.v3 accepts null and yes/on strings for bool fields. Inspect tags before
// decoding consent, while retaining the existing KnownFields decoder unchanged.
func validateCLIHistoryNodes(node *yaml.Node) error {
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := yamlAliasTarget(node.Content[i])
			// Match the decoder's semantic keys, including !!binary spellings.
			var name string
			if key == nil || key.Decode(&name) != nil || name != "cli_history" {
				continue
			}
			value := yamlAliasTarget(node.Content[i+1])
			var consent bool
			if value == nil || value.Kind != yaml.ScalarNode || value.Tag != "!!bool" || value.Decode(&consent) != nil {
				return errors.New("config: cli_history must be a YAML boolean")
			}
		}
	}
	for _, child := range node.Content {
		if err := validateCLIHistoryNodes(child); err != nil {
			return err
		}
	}
	return nil
}

func yamlAliasTarget(node *yaml.Node) *yaml.Node {
	seen := make(map[*yaml.Node]bool)
	for node != nil && node.Kind == yaml.AliasNode {
		if seen[node] {
			return nil
		}
		seen[node] = true
		node = node.Alias
	}
	return node
}

func hasExecutableCredentialCommand(conns []models.Connection) bool {
	for _, conn := range conns {
		if conn.PasswordCommand != "" {
			return true
		}
		if tunnel := conn.SSHTunnel; tunnel != nil && (tunnel.PassphraseCommand != "" || tunnel.SSHPasswordCommand != "") {
			return true
		}
	}
	return false
}
