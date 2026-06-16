package common

import (
	"errors"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
)

var uuidV4Pat = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-4[0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

func TestAppState_GetOrCreateBufferUUID_EmptyConnID(t *testing.T) {
	a := &AppState{}
	got := a.GetOrCreateBufferUUID("")
	require.Equal(t, "", got)
	require.Nil(t, a.LastBufferUUIDs)
}

func TestAppState_GetOrCreateBufferUUID_GeneratesUUIDv4(t *testing.T) {
	a := &AppState{}
	got := a.GetOrCreateBufferUUID("postgres-prod")
	require.NotEmpty(t, got)
	require.True(t, uuidV4Pat.MatchString(got), "%q is not a canonical UUIDv4", got)
}

func TestAppState_GetOrCreateBufferUUID_Idempotent(t *testing.T) {
	a := &AppState{}
	first := a.GetOrCreateBufferUUID("postgres-prod")
	second := a.GetOrCreateBufferUUID("postgres-prod")
	require.Equal(t, first, second)
	require.Len(t, a.LastBufferUUIDs, 1)
}

func TestAppState_GetOrCreateBufferUUID_DistinctConnIDs(t *testing.T) {
	a := &AppState{}
	one := a.GetOrCreateBufferUUID("conn-a")
	two := a.GetOrCreateBufferUUID("conn-b")
	require.NotEqual(t, one, two)
	require.Len(t, a.LastBufferUUIDs, 2)
}

func TestAppState_GetOrCreateBufferUUID_KeyIsSHA256Prefix(t *testing.T) {
	a := &AppState{}
	connID := "postgres-prod"
	a.GetOrCreateBufferUUID(connID)
	expectedKey := connIDHashKey(connID)
	require.Len(t, expectedKey, 16, "sha256 prefix key must be 16 hex chars")
	_, ok := a.LastBufferUUIDs[expectedKey]
	require.True(t, ok, "map key %q missing from LastBufferUUIDs %v", expectedKey, a.LastBufferUUIDs)
}

// failRenameFs wraps an afero.Fs and forces Rename to fail. All other
// operations (MkdirAll, OpenFile, Stat, Remove, WriteFile, etc.) delegate to
// the embedded base. Used to exercise Save's tmp-cleanup-on-rename-failure
// branch without depending on platform-specific fault injection.
type failRenameFs struct {
	afero.Fs
	renameErr error
}

func (f *failRenameFs) Rename(oldname, newname string) error {
	return f.renameErr
}

func (f *failRenameFs) Name() string { return "failRenameFs" }

func TestAppState_SaveLoad_RoundTrip_MemMapFs(t *testing.T) {
	fs := afero.NewMemMapFs()
	const path = "/state.yml"

	a := &AppState{
		HiddenSchemas: map[string][]string{"conn1": {"public", "_internal"}},
		HiddenColumns: map[string]map[string][]string{"conn1": {"tbl": {"x"}}},
	}
	require.NoError(t, a.Save(fs, path))

	b := &AppState{}
	require.NoError(t, b.Load(fs, path))
	require.Equal(t, []string{"public", "_internal"}, b.HiddenSchemas["conn1"])
	require.Equal(t, []string{"x"}, b.HiddenColumns["conn1"]["tbl"])
}

// goldenOldStateYAML is a representative state.yml as written by an OLDER
// binary (note version: 0.1.0), with every recognized AppState field populated
// — including the nested maps. It is a raw on-disk literal (not a marshaled
// struct) because that most faithfully simulates a file produced by a prior
// binary. The guarantee under test: loading it through the CURRENT AppState
// struct and saving it back drops no recognized field (self-update relevance —
// see AppState.Version godoc).
const goldenOldStateYAML = `last_connection_id: conn-prod
recent_connection_ids:
    - conn-prod
    - conn-staging
last_buffer_uuids:
    conn-prod: 11111111-1111-4111-8111-111111111111
    conn-staging: 22222222-2222-4222-8222-222222222222
last_theme: dracula
last_result_view_mode: expanded
startup_tips_seen_at: 2024-01-02T03:04:05Z
version: 0.1.0
statement_timeout_override:
    conn-prod: 30s
    conn-staging: 10s
hidden_schemas:
    conn-prod:
        - pg_catalog
        - information_schema
hidden_columns:
    conn-prod:
        users:
            - password_hash
            - ssn
        orders:
            - internal_note
last_session_settings:
    conn-prod:
        search_path: public
        timezone: UTC
last_schema_name:
    conn-prod: public
last_table_name:
    conn-prod: users
`

func TestAppState_GoldenOldState_RoundTripDropsNoField(t *testing.T) {
	tipsSeen, err := time.Parse(time.RFC3339, "2024-01-02T03:04:05Z")
	require.NoError(t, err)

	want := AppState{
		LastConnectionID:    "conn-prod",
		RecentConnectionIDs: []string{"conn-prod", "conn-staging"},
		LastBufferUUIDs: map[string]string{
			"conn-prod":    "11111111-1111-4111-8111-111111111111",
			"conn-staging": "22222222-2222-4222-8222-222222222222",
		},
		LastTheme:          "dracula",
		LastResultViewMode: "expanded",
		StartupTipsSeenAt:  tipsSeen,
		Version:            "0.1.0",
		StatementTimeoutOverride: map[string]string{
			"conn-prod":    "30s",
			"conn-staging": "10s",
		},
		HiddenSchemas: map[string][]string{
			"conn-prod": {"pg_catalog", "information_schema"},
		},
		HiddenColumns: map[string]map[string][]string{
			"conn-prod": {
				"users":  {"password_hash", "ssn"},
				"orders": {"internal_note"},
			},
		},
		LastSessionSettings: map[string]map[string]string{
			"conn-prod": {"search_path": "public", "timezone": "UTC"},
		},
		LastSchemaName: map[string]string{"conn-prod": "public"},
		LastTableName:  map[string]string{"conn-prod": "users"},
	}

	fs := afero.NewMemMapFs()
	const oldPath = "/old-state.yml"
	require.NoError(t, afero.WriteFile(fs, oldPath, []byte(goldenOldStateYAML), 0o600))

	// Load the older file through the current struct: no panic, no error.
	var loaded AppState
	require.NoError(t, loaded.Load(fs, oldPath))
	require.Equal(t, want, loaded, "older state.yml must load into the current struct without dropping fields")

	// Save it back to a new path with the current binary, reload, and assert
	// every recognized field survives the round-trip.
	const newPath = "/new-state.yml"
	require.NoError(t, loaded.Save(fs, newPath))

	var reloaded AppState
	require.NoError(t, reloaded.Load(fs, newPath))
	require.Equal(t, want, reloaded, "Load→Save→Load round-trip must not drop any recognized field")
}

func TestAppState_Save_TmpAbsentAfterSuccess(t *testing.T) {
	fs := afero.NewMemMapFs()
	const path = "/state.yml"
	a := &AppState{LastConnectionID: "abc"}
	require.NoError(t, a.Save(fs, path))

	_, err := fs.Stat(path + ".tmp")
	require.True(t, os.IsNotExist(err), "expected tmp file absent, got err=%v", err)
}

func TestAppState_Save_FileMode0600_MemMapFs(t *testing.T) {
	fs := afero.NewMemMapFs()
	const path = "/state.yml"
	require.NoError(t, (&AppState{LastConnectionID: "x"}).Save(fs, path))

	info, err := fs.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestAppState_Load_CorruptYAML_ReturnsError(t *testing.T) {
	fs := afero.NewMemMapFs()
	const path = "/state.yml"
	require.NoError(t, afero.WriteFile(fs, path, []byte("not: valid: yaml: ::"), 0o600))

	a := &AppState{}
	err := a.Load(fs, path)
	require.Error(t, err)
}

func TestAppState_Load_MissingFile_ReturnsNilZeroState(t *testing.T) {
	fs := afero.NewMemMapFs()
	a := &AppState{}
	require.NoError(t, a.Load(fs, "/missing.yml"))
	// Zero value preserved.
	require.Equal(t, "", a.LastConnectionID)
	require.Nil(t, a.RecentConnectionIDs)
	require.Nil(t, a.HiddenSchemas)
}

func TestAppState_Save_TmpCleanupOnRenameFailure(t *testing.T) {
	base := afero.NewMemMapFs()
	fs := &failRenameFs{Fs: base, renameErr: errors.New("synthetic rename failure")}

	a := &AppState{LastConnectionID: "x"}
	err := a.Save(fs, "/state.yml")
	require.Error(t, err)
	require.Contains(t, err.Error(), "rename")
	require.Contains(t, err.Error(), "synthetic rename failure")

	// Tmp file removed best-effort. Stat against the underlying base fs.
	_, statErr := base.Stat("/state.yml.tmp")
	require.True(t, os.IsNotExist(statErr), "expected tmp file removed, got err=%v", statErr)

	// Destination unchanged (never existed).
	_, statErr = base.Stat("/state.yml")
	require.True(t, os.IsNotExist(statErr))
}

func TestAppState_Save_ReadOnlyFs_ReturnsError(t *testing.T) {
	base := afero.NewMemMapFs()
	ro := afero.NewReadOnlyFs(base)
	a := &AppState{LastConnectionID: "x"}
	err := a.Save(ro, "/state.yml")
	require.Error(t, err)

	// Destination never created; tmp never created.
	_, statErr := base.Stat("/state.yml")
	require.True(t, os.IsNotExist(statErr))
	_, statErr = base.Stat("/state.yml.tmp")
	require.True(t, os.IsNotExist(statErr))
}

// PushRecentConnectionID prepends, dedupes, and caps the
// returned slice at 10 entries.

func TestPushRecentConnectionID_EmptyConnIDLeavesSliceUnchanged(t *testing.T) {
	in := []string{"a", "b"}
	out := PushRecentConnectionID(in, "")
	require.Equal(t, in, out)
	// Defensive copy: the returned slice does not alias the input so
	// callers may safely assign it under MutateAndSave.
	out[0] = "MUTATED"
	require.Equal(t, []string{"a", "b"}, in)
}

func TestPushRecentConnectionID_PrependsNewID(t *testing.T) {
	out := PushRecentConnectionID([]string{"a", "b"}, "c")
	require.Equal(t, []string{"c", "a", "b"}, out)
}

func TestPushRecentConnectionID_DedupesAndMovesToFront(t *testing.T) {
	out := PushRecentConnectionID([]string{"a", "b", "c"}, "b")
	require.Equal(t, []string{"b", "a", "c"}, out)
}

func TestPushRecentConnectionID_CapsAtTen(t *testing.T) {
	prior := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	out := PushRecentConnectionID(prior, "z")
	require.Len(t, out, 10)
	require.Equal(t, "z", out[0])
	// Tail entry from the prior slice ("j") falls off.
	require.Equal(t, "i", out[len(out)-1])
}

func TestPushRecentConnectionID_OrderingAfterRepeatedPushes(t *testing.T) {
	var recent []string
	recent = PushRecentConnectionID(recent, "a")
	recent = PushRecentConnectionID(recent, "b")
	recent = PushRecentConnectionID(recent, "c")
	recent = PushRecentConnectionID(recent, "a") // re-add bumps a to front
	require.Equal(t, []string{"a", "c", "b"}, recent)
}
