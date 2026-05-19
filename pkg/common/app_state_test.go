package common

import (
	"errors"
	"os"
	"regexp"
	"testing"

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
