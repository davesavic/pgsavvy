package common

import (
	"errors"
	"os"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
)

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
