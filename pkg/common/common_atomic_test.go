package common

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/davesavic/dbsavvy/pkg/config"
)

func TestCommon_UserConfigStore_RoundTrip(t *testing.T) {
	c := NewDummyCommon()
	cfg2 := config.GetDefaultConfig()
	cfg2.Leader = "<f1>"
	c.UserConfig.Store(cfg2)
	require.Same(t, cfg2, c.Cfg())
	require.Equal(t, "<f1>", c.Cfg().Leader)
}
