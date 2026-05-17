package common

import (
	"io"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/i18n"
)

func newSilentLogger() *logrus.Logger {
	l := logrus.New()
	l.SetOutput(io.Discard)
	l.SetLevel(logrus.PanicLevel)
	return l
}

func TestNewCommon_ConstructsWithNonNilFields(t *testing.T) {
	log := newSilentLogger()
	tr := i18n.EnglishTranslationSet()
	cfg := config.GetDefaultConfig()
	app := &AppState{}
	fs := afero.NewMemMapFs()

	c := NewCommon(log, tr, cfg, app, fs)
	require.NotNil(t, c)
	require.NotNil(t, c.Log)
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
	require.Same(t, log, c.Log)
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

func TestNewCommon_GOOSWarning_Windows(t *testing.T) {
	// Capture and restore getGOOS.
	saved := getGOOS
	t.Cleanup(func() { getGOOS = saved })
	getGOOS = func() string { return "windows" }

	logger := logrus.New()
	logger.SetOutput(io.Discard)
	logger.SetLevel(logrus.WarnLevel)
	hook := logrustest.NewLocal(logger)

	c := NewCommon(logger, i18n.EnglishTranslationSet(), config.GetDefaultConfig(), &AppState{}, afero.NewMemMapFs())
	require.NotNil(t, c)

	entries := hook.AllEntries()
	require.NotEmpty(t, entries, "expected at least one log entry from NewCommon")
	found := false
	for _, e := range entries {
		if e.Level == logrus.WarnLevel && strings.Contains(e.Message, "OS not officially supported") {
			found = true
			break
		}
	}
	require.True(t, found, "expected warning containing 'OS not officially supported', got entries: %+v", entries)
}

func TestNewCommon_GOOSWarning_Linux_NoWarning(t *testing.T) {
	saved := getGOOS
	t.Cleanup(func() { getGOOS = saved })
	getGOOS = func() string { return "linux" }

	logger := logrus.New()
	logger.SetOutput(io.Discard)
	logger.SetLevel(logrus.DebugLevel)
	hook := logrustest.NewLocal(logger)

	_ = NewCommon(logger, i18n.EnglishTranslationSet(), config.GetDefaultConfig(), &AppState{}, afero.NewMemMapFs())
	for _, e := range hook.AllEntries() {
		require.NotContains(t, e.Message, "OS not officially supported")
	}
}

func TestNewCommon_GOOSWarning_Darwin_NoWarning(t *testing.T) {
	saved := getGOOS
	t.Cleanup(func() { getGOOS = saved })
	getGOOS = func() string { return "darwin" }

	logger := logrus.New()
	logger.SetOutput(io.Discard)
	logger.SetLevel(logrus.DebugLevel)
	hook := logrustest.NewLocal(logger)

	_ = NewCommon(logger, i18n.EnglishTranslationSet(), config.GetDefaultConfig(), &AppState{}, afero.NewMemMapFs())
	for _, e := range hook.AllEntries() {
		require.NotContains(t, e.Message, "OS not officially supported")
	}
}
