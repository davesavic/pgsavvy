package common

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
)

func TestAppState_SaveLoad_RoundTrip_RealFs(t *testing.T) {
	tmpdir := t.TempDir()
	fs := afero.NewBasePathFs(afero.NewOsFs(), tmpdir)
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

	// Mode check via the real os.Stat — afero.BasePathFs maps "/state.yml" to
	// filepath.Join(tmpdir, "state.yml").
	info, err := os.Stat(filepath.Join(tmpdir, "state.yml"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// Tmp file must not be left behind on success.
	_, statErr := os.Stat(filepath.Join(tmpdir, "state.yml.tmp"))
	require.True(t, os.IsNotExist(statErr))
}
