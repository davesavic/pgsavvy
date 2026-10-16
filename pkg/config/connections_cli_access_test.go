package config

import (
	"bytes"
	"errors"
	"testing"

	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/models"
)

func TestUpdateConnectionWithCLIAccess(t *testing.T) {
	for _, original := range []models.CLIAccess{"", models.CLIAccessDisabled, models.CLIAccessMetadata, models.CLIAccessRowRead} {
		for _, selected := range []models.CLIAccess{models.CLIAccessDisabled, models.CLIAccessMetadata, models.CLIAccessRowRead} {
			t.Run(string(original)+"_to_"+string(selected), func(t *testing.T) {
				fs := afero.NewMemMapFs()
				const path = "/private/connections.yml"
				if err := SaveConnections(fs, path, []models.Connection{{Name: "dev", CLIAccess: original}, {Name: "other", CLIAccess: models.CLIAccessMetadata}}); err != nil {
					t.Fatal(err)
				}
				conn := models.Connection{Name: "renamed", Label: "edited", CLIAccess: selected}
				if err := UpdateConnectionWithCLIAccess(fs, path, "dev", conn, original); err != nil {
					t.Fatal(err)
				}
				got, err := LoadConnections(fs, path)
				if err != nil || len(got) != 2 || got[0].Name != conn.Name || got[0].Label != conn.Label || got[0].CLIAccess != selected || got[1].CLIAccess != models.CLIAccessMetadata {
					t.Fatalf("saved profiles = %v, error = %v", got, err)
				}
			})
		}
	}
}

func TestUpdateConnectionWithCLIAccessRejectsUnsafeEdits(t *testing.T) {
	for _, tc := range []struct {
		name     string
		oldName  string
		newName  string
		expected models.CLIAccess
		selected models.CLIAccess
		wantErr  error
	}{
		{"stale-policy", "dev", "renamed", models.CLIAccessRowRead, models.CLIAccessMetadata, ErrConnectionsChanged},
		{"invalid-policy", "dev", "dev", models.CLIAccessDisabled, "unrestricted", nil},
		{"rename-collision", "dev", "other", models.CLIAccessDisabled, models.CLIAccessRowRead, ErrDuplicateConnectionName},
		{"missing-profile", "missing", "renamed", models.CLIAccessDisabled, models.CLIAccessRowRead, ErrConnectionNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			const path = "/private/connections.yml"
			if err := SaveConnections(fs, path, []models.Connection{{Name: "dev", CLIAccess: models.CLIAccessDisabled}, {Name: "other"}}); err != nil {
				t.Fatal(err)
			}
			before, err := afero.ReadFile(fs, path)
			if err != nil {
				t.Fatal(err)
			}
			err = UpdateConnectionWithCLIAccess(fs, path, tc.oldName, models.Connection{Name: tc.newName, CLIAccess: tc.selected}, tc.expected)
			if err == nil || (tc.wantErr != nil && !errors.Is(err, tc.wantErr)) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			after, err := afero.ReadFile(fs, path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("rejected edit changed the file")
			}
		})
	}
}

func TestUpdateConnectionWithCLIAccessRejectsInterveningRevocation(t *testing.T) {
	base := afero.NewMemMapFs()
	const path = "/private/connections.yml"
	if err := SaveConnections(base, path, []models.Connection{{Name: "dev", CLIAccess: models.CLIAccessMetadata}}); err != nil {
		t.Fatal(err)
	}
	revoked := []byte("connections:\n  - name: dev\n    cli_access: disabled\n")
	fs := &changingConnectionFs{Fs: base, change: func() {
		if err := afero.WriteFile(base, path, revoked, 0o600); err != nil {
			t.Fatal(err)
		}
	}}
	err := UpdateConnectionWithCLIAccess(fs, path, "dev", models.Connection{Name: "renamed", CLIAccess: models.CLIAccessRowRead}, models.CLIAccessMetadata)
	if !errors.Is(err, ErrConnectionsChanged) {
		t.Fatalf("error = %v, want ErrConnectionsChanged", err)
	}
	after, err := afero.ReadFile(base, path)
	if err != nil || !bytes.Equal(after, revoked) {
		t.Fatal("intervening revocation was overwritten")
	}
	entries, err := afero.ReadDir(base, "/private")
	if err != nil || len(entries) != 1 || entries[0].Name() != "connections.yml" {
		t.Fatalf("temporary file leaked: %v, %v", entries, err)
	}
}
