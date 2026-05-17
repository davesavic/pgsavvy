package common

import (
	"errors"
	"os"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/dbsavvy/pkg/config"
)

func TestNewDummyCommon_RoundTrip(t *testing.T) {
	c := NewDummyCommon()
	require.NotNil(t, c)
	require.NotNil(t, c.Log)
	require.NotNil(t, c.Tr)
	require.NotNil(t, c.AppState)
	require.NotNil(t, c.Fs)
	require.NotNil(t, c.UserConfig.Load())

	// English baseline reached. Asserts the shipped string from pkg/i18n.
	require.Equal(t, "Open Table", c.Tr.OpenTable)

	// Cfg matches the built-in default on at least Leader and Timeout.
	want := config.GetDefaultConfig()
	got := c.Cfg()
	require.Equal(t, want.Leader, got.Leader)
	require.Equal(t, want.Timeout, got.Timeout)

	// Fs is MemMapFs — Stat on a never-written path returns not-exists.
	_, err := c.Fs.Stat("/no-such-file")
	require.True(t, errors.Is(err, afero.ErrFileNotFound) || os.IsNotExist(err), "expected not-exist error, got %v", err)
}
