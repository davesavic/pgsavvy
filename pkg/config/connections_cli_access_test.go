package config

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/models"
)

func TestUpdateConnectionWithCLIAccess(t *testing.T) {
	for _, original := range []models.CLIAccess{"", models.CLIAccessDisabled, models.CLIAccessMetadata, models.CLIAccessRowRead, models.CLIAccessFull} {
		for _, selected := range []models.CLIAccess{models.CLIAccessDisabled, models.CLIAccessMetadata, models.CLIAccessRowRead, models.CLIAccessFull} {
			t.Run(string(original)+"_to_"+string(selected), func(t *testing.T) {
				fs := newConnectionsTestFs()
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

func TestUpdateConnectionFullCooperatingRevocation(t *testing.T) {
	for _, revocation := range []string{"access", "history", "both"} {
		for _, edit := range []string{"ordinary", "access", "combined"} {
			t.Run(revocation+"/"+edit, func(t *testing.T) {
				fs := newConnectionsTestFs()
				const path = "/private/connections.yml"
				initial := models.Connection{Name: "dev", CLIAccess: models.CLIAccessFull, CLIHistory: true, ConfirmWrites: true, ConfirmDDL: true}
				neighbor := models.Connection{Name: "other", CLIAccess: models.CLIAccessMetadata, CLIHistory: true}
				if err := SaveConnections(fs, path, []models.Connection{initial, neighbor}); err != nil {
					t.Fatal(err)
				}
				// A cooperating save revokes consent after the stale form was opened.
				revoked := initial
				if revocation != "history" {
					revoked.CLIAccess = models.CLIAccessDisabled
				}
				if revocation != "access" {
					revoked.CLIHistory = false
				}
				if err := UpdateConnectionWithCLISettings(fs, path, "dev", revoked, &initial.CLIAccess, &initial.CLIHistory); err != nil {
					t.Fatal(err)
				}
				before, err := afero.ReadFile(fs, path)
				if err != nil {
					t.Fatal(err)
				}
				stale := initial
				stale.Name, stale.Label = "renamed", "edited"
				var access *models.CLIAccess
				var history *bool
				if edit != "ordinary" {
					access = &initial.CLIAccess
					stale.CLIAccess = models.CLIAccessMetadata
				}
				if edit == "combined" {
					history = &initial.CLIHistory
				}
				err = UpdateConnectionWithCLISettings(fs, path, "dev", stale, access, history)
				conflict := edit == "combined" || (edit == "access" && revocation != "history")
				if conflict {
					if !errors.Is(err, ErrConnectionsChanged) {
						t.Fatalf("stale explicit edit error = %v", err)
					}
					after, readErr := afero.ReadFile(fs, path)
					if readErr != nil || !bytes.Equal(before, after) {
						t.Fatal("stale explicit edit changed file")
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					revoked.Name, revoked.Label = stale.Name, stale.Label
					if access != nil {
						revoked.CLIAccess = stale.CLIAccess
					}
				}
				got, err := LoadConnections(fs, path)
				if err != nil || len(got) != 2 || !reflect.DeepEqual(got[0], revoked) || !reflect.DeepEqual(got[1], neighbor) {
					t.Fatalf("revocation or neighboring profile changed: %v, %v", got, err)
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
			fs := newConnectionsTestFs()
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
	base := newConnectionsTestFs()
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
