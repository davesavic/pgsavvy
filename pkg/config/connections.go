package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"

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
	return LoadConnectionsContext(context.Background(), fs, path, warnings)
}

// LoadConnectionsContext preserves strict loading while allowing cancellation
// during input/parsing/validation. The filesystem must provide interruptible
// reads for blocked I/O; the CLI supplies an already bounded memory snapshot.
func LoadConnectionsContext(ctx context.Context, fs afero.Fs, path string, warnings io.Writer) ([]models.Connection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
	data, err := io.ReadAll(connectionsContextReader{ctx, file})
	if err != nil {
		return nil, fmt.Errorf("config: read %q: %w", path, err)
	}
	return decodeConnectionsContext(ctx, data, info, path, warnings)
}

func decodeConnections(data []byte, info os.FileInfo, path string) ([]models.Connection, error) {
	return decodeConnectionsWithWarnings(data, info, path, warnWriter)
}

func decodeConnectionsWithWarnings(data []byte, info os.FileInfo, path string, warnings io.Writer) ([]models.Connection, error) {
	return decodeConnectionsContext(context.Background(), data, info, path, warnings)
}

type connectionsContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r connectionsContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.r.Read(p)
	if canceled := r.ctx.Err(); canceled != nil {
		return 0, canceled
	}
	return n, err
}

func decodeConnectionsContext(ctx context.Context, data []byte, info os.FileInfo, path string, warnings io.Writer) ([]models.Connection, error) {
	var doc yaml.Node
	if err := yaml.NewDecoder(connectionsContextReader{ctx, bytes.NewReader(data)}).Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("config: parse %q: %w", path, err)
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 && doc.Content[0].Kind == yaml.SequenceNode {
		return nil, fmt.Errorf(
			"config: %q is in legacy flat format (top-level sequence); expected key 'connections:' — see DESIGN.md §11.2.\nConvert your file to wrapper form:\n%s",
			path, legacyMigrationSnippet,
		)
	}

	if err := validateCLIHistoryNodes(ctx, &doc); err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(connectionsContextReader{ctx, bytes.NewReader(data)})
	dec.KnownFields(true)
	var wrapper connectionsFile
	if err := dec.Decode(&wrapper); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, io.EOF) {
			return []models.Connection{}, nil
		}
		return nil, fmt.Errorf("config: parse %q: %w", path, err)
	}
	if wrapper.Connections == nil {
		return []models.Connection{}, nil
	}
	for _, conn := range wrapper.Connections {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
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
// decoding consent, while retaining KnownFields. Wide mappings would enter
// yaml.v3's quadratic, non-cancellable duplicate-key conversion; every valid
// mapping fits a known struct's fields plus the YAML merge key.
func validateCLIHistoryNodes(ctx context.Context, node *yaml.Node) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if node.Kind == yaml.MappingNode {
		maxFields := max(reflect.TypeFor[connectionsFile]().NumField(), reflect.TypeFor[models.Connection]().NumField(), reflect.TypeFor[models.SSHTunnelConfig]().NumField()) + 1
		if len(node.Content)/2 > maxFields {
			return errors.New("config: too many connection mapping fields")
		}
		for i := 0; i+1 < len(node.Content); i += 2 {
			if err := ctx.Err(); err != nil {
				return err
			}
			key := yamlAliasTarget(node.Content[i])
			// Match the decoder's semantic keys, including !!binary spellings.
			var name string
			if key == nil || key.Kind != yaml.ScalarNode || key.Decode(&name) != nil || name != "cli_history" {
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
		if err := validateCLIHistoryNodes(ctx, child); err != nil {
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
