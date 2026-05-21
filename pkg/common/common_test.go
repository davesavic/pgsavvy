package common

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/logs"
)

func newSilentLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestNewCommon_ConstructsWithNonNilFields(t *testing.T) {
	log := newSilentLogger()
	tr := i18n.EnglishTranslationSet()
	cfg := config.GetDefaultConfig()
	app := &AppState{}
	fs := afero.NewMemMapFs()

	c := NewCommon(log, tr, cfg, app, fs)
	require.NotNil(t, c)
	require.NotNil(t, c.Logger())
	require.NotNil(t, c.Tr)
	require.NotNil(t, c.AppState)
	require.NotNil(t, c.Fs)
	require.NotNil(t, c.UserConfig.Load())
}

func TestNewCommon_ParameterOrder_PointerEquality(t *testing.T) {
	log := newSilentLogger()
	tr := i18n.EnglishTranslationSet()
	cfg := config.GetDefaultConfig()
	app := &AppState{}
	fs := afero.NewMemMapFs()

	c := NewCommon(log, tr, cfg, app, fs)
	require.Same(t, log, c.Logger())
	require.Same(t, tr, c.Tr)
	require.Same(t, app, c.AppState)
	require.Same(t, cfg, c.UserConfig.Load())
	// afero.Fs is an interface; compare via underlying identity through Cfg-style equality.
	require.Equal(t, fs, c.Fs)
}

func TestNewCommon_NilCfg_Panics(t *testing.T) {
	log := newSilentLogger()
	tr := i18n.EnglishTranslationSet()
	app := &AppState{}
	fs := afero.NewMemMapFs()

	require.PanicsWithValue(t, "NewCommon: cfg is nil", func() {
		NewCommon(log, tr, nil, app, fs)
	})
}

// TestNewCommon_NilLog_Panics verifies AD-A4: NewCommon refuses a nil
// *slog.Logger. The cfg-nil check still runs first (AMD-F2-5), so this
// test supplies a non-nil cfg.
func TestNewCommon_NilLog_Panics(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	cfg := config.GetDefaultConfig()
	app := &AppState{}
	fs := afero.NewMemMapFs()

	require.PanicsWithValue(t, "NewCommon: log is nil", func() {
		NewCommon(nil, tr, cfg, app, fs)
	})
}

// TestNewCommon_BothNil_PanicsOnCfgFirst documents the ordering invariant
// from AMD-F2-5: when both cfg and log are nil, the cfg panic message wins.
func TestNewCommon_BothNil_PanicsOnCfgFirst(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	app := &AppState{}
	fs := afero.NewMemMapFs()

	require.PanicsWithValue(t, "NewCommon: cfg is nil", func() {
		NewCommon(nil, tr, nil, app, fs)
	})
}

func TestCommon_Logger_NilSafe(t *testing.T) {
	var c *Common
	require.NotNil(t, c.Logger())
	// Calling against a zero-value Common (no log field set) also returns a
	// non-nil discarding logger.
	z := &Common{}
	require.NotNil(t, z.Logger())
}

func TestNewCommon_GOOSWarning_Windows(t *testing.T) {
	// Capture and restore getGOOS.
	saved := getGOOS
	t.Cleanup(func() { getGOOS = saved })
	getGOOS = func() string { return "windows" }

	rh := logs.NewRecordingHandler()
	logger := slog.New(rh)

	c := NewCommon(logger, i18n.EnglishTranslationSet(), config.GetDefaultConfig(), &AppState{}, afero.NewMemMapFs())
	require.NotNil(t, c)

	records := rh.Records()
	require.NotEmpty(t, records, "expected at least one log record from NewCommon")
	found := false
	for _, r := range records {
		if r.Level == slog.LevelWarn && strings.Contains(r.Message, "OS not officially supported") {
			found = true
			break
		}
	}
	require.True(t, found, "expected warning containing 'OS not officially supported'; records: %+v", records)
}

func TestNewCommon_GOOSWarning_Linux_NoWarning(t *testing.T) {
	saved := getGOOS
	t.Cleanup(func() { getGOOS = saved })
	getGOOS = func() string { return "linux" }

	rh := logs.NewRecordingHandler()
	logger := slog.New(rh)

	_ = NewCommon(logger, i18n.EnglishTranslationSet(), config.GetDefaultConfig(), &AppState{}, afero.NewMemMapFs())
	for _, r := range rh.Records() {
		require.NotContains(t, r.Message, "OS not officially supported")
	}
}

func TestNewCommon_GOOSWarning_Darwin_NoWarning(t *testing.T) {
	saved := getGOOS
	t.Cleanup(func() { getGOOS = saved })
	getGOOS = func() string { return "darwin" }

	rh := logs.NewRecordingHandler()
	logger := slog.New(rh)

	_ = NewCommon(logger, i18n.EnglishTranslationSet(), config.GetDefaultConfig(), &AppState{}, afero.NewMemMapFs())
	for _, r := range rh.Records() {
		require.NotContains(t, r.Message, "OS not officially supported")
	}
}
