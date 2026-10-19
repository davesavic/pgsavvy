package config

import (
	"bytes"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/models"
)

func TestCLIHistoryConsent_RoundTrip(t *testing.T) {
	for _, history := range []bool{false, true} {
		for _, access := range []models.CLIAccess{"", models.CLIAccessDisabled, models.CLIAccessMetadata, models.CLIAccessRowRead} {
			for _, readOnly := range []bool{false, true} {
				fs := newConnectionsTestFs()
				want := []models.Connection{{Name: "dev", CLIHistory: history, CLIAccess: access, ReadOnly: readOnly}}
				seedConns(t, fs, "/c.yml", want)
				got, err := LoadConnections(fs, "/c.yml")
				if err != nil || !reflect.DeepEqual(want, got) {
					t.Fatalf("consent round trip: %v, %v", got, err)
				}
				body, err := afero.ReadFile(fs, "/c.yml")
				if err != nil || bytes.Contains(body, []byte("cli_history:")) != history {
					t.Fatalf("consent omission/encoding: %s, %v", body, err)
				}
			}
		}
	}
}

func TestCLIHistoryConsent_StaleEditPreservation(t *testing.T) {
	for _, rename := range []bool{false, true} {
		fs := newConnectionsTestFs()
		initial := models.Connection{Name: "dev", CLIHistory: true, CLIAccess: models.CLIAccessRowRead}
		neighbor := models.Connection{Name: "other", CLIHistory: true, CLIAccess: models.CLIAccessMetadata}
		seedConns(t, fs, "/c.yml", []models.Connection{initial, neighbor})
		revoked := initial
		revoked.CLIHistory, revoked.CLIAccess = false, models.CLIAccessDisabled
		if err := UpdateConnectionWithCLISettings(fs, "/c.yml", "dev", revoked, &initial.CLIAccess, &initial.CLIHistory); err != nil {
			t.Fatal(err)
		}
		stale := initial
		stale.Label = "edited"
		if rename {
			stale.Name = "renamed"
		}
		if err := UpdateConnection(fs, "/c.yml", "dev", stale); err != nil {
			t.Fatal(err)
		}
		got, err := LoadConnections(fs, "/c.yml")
		if err != nil || len(got) != 2 || got[0].CLIHistory || got[0].CLIAccess != models.CLIAccessDisabled || got[0].Name != stale.Name || got[0].Label != "edited" || !reflect.DeepEqual(got[1], neighbor) {
			t.Fatalf("stale edit restored policy or changed neighbor: %v, %v", got, err)
		}
		stale.CLIAccess = models.CLIAccessMetadata
		if err := UpdateConnectionWithCLIAccess(fs, "/c.yml", stale.Name, stale, models.CLIAccessDisabled); err != nil {
			t.Fatal(err)
		}
		got, err = LoadConnections(fs, "/c.yml")
		if err != nil || got[0].CLIHistory || got[0].CLIAccess != models.CLIAccessMetadata {
			t.Fatalf("access-only wrapper restored history: %v, %v", got, err)
		}
	}
}

func TestCLIHistoryConsent_AtomicPolicyChanges(t *testing.T) {
	for _, changeAccess := range []bool{false, true} {
		for _, changeHistory := range []bool{false, true} {
			fs := newConnectionsTestFs()
			initial := models.Connection{Name: "dev", CLIHistory: true, CLIAccess: models.CLIAccessRowRead}
			seedConns(t, fs, "/c.yml", []models.Connection{initial})
			var expectedAccess *models.CLIAccess
			var expectedHistory *bool
			if changeAccess {
				expectedAccess = &initial.CLIAccess
			}
			if changeHistory {
				expectedHistory = &initial.CLIHistory
			}
			next := models.Connection{Name: "renamed", Label: "edited", CLIHistory: false, CLIAccess: models.CLIAccessMetadata, Host: "localhost", DSN: "postgres://legacy/db"}
			if err := UpdateConnectionWithCLISettings(fs, "/c.yml", "dev", next, expectedAccess, expectedHistory); err != nil {
				t.Fatal(err)
			}
			got, err := LoadConnections(fs, "/c.yml")
			wantAccess := initial.CLIAccess
			if changeAccess {
				wantAccess = next.CLIAccess
			}
			if err != nil || len(got) != 1 || got[0].CLIHistory != !changeHistory || got[0].CLIAccess != wantAccess || got[0].Name != next.Name || got[0].Label != next.Label || got[0].DSN != "" {
				t.Fatalf("field-specific edit/normalization: %v, %v", got, err)
			}
		}
	}
	// Enable consent on exactly one profile without granting any CLI access.
	fs := newConnectionsTestFs()
	seedConns(t, fs, "/c.yml", []models.Connection{{Name: "dev"}, {Name: "other"}})
	expected := false
	if err := UpdateConnectionWithCLISettings(fs, "/c.yml", "dev", models.Connection{Name: "dev", CLIHistory: true}, nil, &expected); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConnections(fs, "/c.yml")
	if err != nil || len(got) != 2 || !got[0].CLIHistory || got[1].CLIHistory || got[0].EffectiveCLIAccess() != models.CLIAccessDisabled {
		t.Fatalf("consent altered other policy/profile: %v, %v", got, err)
	}
}

func TestCLIHistoryConsent_ConflictsAndFailures(t *testing.T) {
	for _, failure := range []string{"access-conflict", "history-conflict", "missing", "duplicate", "collision", "invalid-access", "read-only", "rename-failure", "contention", "intervening-revocation"} {
		t.Run(failure, func(t *testing.T) {
			base := newConnectionsTestFs()
			initial := models.Connection{Name: "dev", CLIHistory: true, CLIAccess: models.CLIAccessRowRead}
			conns := []models.Connection{initial, {Name: "other", CLIHistory: true}}
			if failure == "duplicate" {
				conns = append(conns, initial)
			}
			seedConns(t, base, "/c.yml", conns)
			before, err := afero.ReadFile(base, "/c.yml")
			if err != nil {
				t.Fatal(err)
			}
			fs := afero.Fs(base)
			access, history := initial.CLIAccess, initial.CLIHistory
			next := models.Connection{Name: "renamed", CLIHistory: false, CLIAccess: models.CLIAccessMetadata}
			oldName, wantErr := "dev", error(nil)
			switch failure {
			case "access-conflict":
				access, wantErr = models.CLIAccessDisabled, ErrConnectionsChanged
			case "history-conflict":
				history, wantErr = false, ErrConnectionsChanged
			case "missing":
				oldName, wantErr = "missing", ErrConnectionNotFound
			case "duplicate":
				wantErr = ErrDuplicateConnectionName
			case "collision":
				next.Name, wantErr = "other", ErrDuplicateConnectionName
			case "invalid-access":
				next.CLIAccess = "unrestricted"
			case "read-only":
				fs = &connectionsTestFs{Fs: afero.NewReadOnlyFs(base), lock: base.lock}
			case "rename-failure":
				fs = &renameFailFs{Fs: base}
			case "contention":
				base.lock <- struct{}{}
				defer func() { <-base.lock }()
				wantErr = os.ErrDeadlineExceeded
			case "intervening-revocation":
				before = []byte("connections:\n  - name: dev\n    cli_history: false\n    cli_access: disabled\n")
				fs = &changingConnectionFs{Fs: base, change: func() {
					if err := afero.WriteFile(base, "/c.yml", before, 0o600); err != nil {
						t.Fatal(err)
					}
				}}
				wantErr = ErrConnectionsChanged
			}
			err = UpdateConnectionWithCLISettings(fs, "/c.yml", oldName, next, &access, &history)
			if err == nil || (wantErr != nil && !errors.Is(err, wantErr)) {
				t.Fatalf("failure not preserved: %v; want %v", err, wantErr)
			}
			if failure == "read-only" && !strings.Contains(err.Error(), "atomic_yaml: mkdir") {
				t.Fatalf("read-only publication not reached: %v", err)
			}
			if failure == "rename-failure" && !strings.Contains(err.Error(), "simulated rename failure") {
				t.Fatalf("rename failure not reached: %v", err)
			}
			after, err := afero.ReadFile(base, "/c.yml")
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("failed policy edit changed bytes: %v", err)
			}
		})
	}
}
