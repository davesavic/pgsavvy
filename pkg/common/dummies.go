package common

import (
	"io"

	"github.com/sirupsen/logrus"
	"github.com/spf13/afero"

	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/i18n"
)

// NewDummyCommon returns a fully wired *Common suitable for tests. The logger
// discards output at PanicLevel, the translation set is the English baseline,
// the user config is the built-in default, the app state is zero-value, and
// the filesystem is an in-memory afero.Fs. The returned bag is independent on
// every call; callers may mutate fields without disturbing other callers.
func NewDummyCommon() *Common {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	logger.SetLevel(logrus.PanicLevel)
	tr := i18n.EnglishTranslationSet()
	cfg := config.GetDefaultConfig()
	app := &AppState{}
	fs := afero.NewMemMapFs()
	return NewCommon(logger, tr, cfg, app, fs)
}
