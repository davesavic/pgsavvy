package common

import (
	"runtime"
	"sync/atomic"

	"github.com/sirupsen/logrus"
	"github.com/spf13/afero"

	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/i18n"
)

// getGOOS is the indirection point for the runtime.GOOS lookup used by
// NewCommon's "supported OS" warning. Tests override this variable to verify
// the warning path without depending on the host OS. Package-private by
// design — downstream code should not depend on this seam.
var getGOOS = func() string { return runtime.GOOS }

// Common is the cross-cutting dependency bag passed by pointer to virtually
// every receiver downstream. The field layout is FROZEN for the foundation
// epic; downstream extensions require epic-level review. See DESIGN.md §5 and
// §15.3.
type Common struct {
	Log *logrus.Logger
	Tr  *i18n.TranslationSet
	// UserConfig holds the live config pointer. Always call Common.Cfg() at point
	// of use. Never cache the *config.UserConfig across hot-reload boundaries —
	// the pointer is swapped on reload. See DESIGN.md §15.3.
	UserConfig atomic.Pointer[config.UserConfig]
	AppState   *AppState
	Fs         afero.Fs
}

// NewCommon constructs a *Common wired to the supplied dependencies. It panics
// with the message "NewCommon: cfg is nil" if cfg is nil — the live config
// pointer is a non-optional invariant of the bag. Before returning, the
// supplied cfg is published via UserConfig.Store so that c.Cfg() is
// immediately safe to call. If the runtime OS is not in {"linux","darwin"} a
// warning is logged via the supplied logger to flag that the platform is not
// officially supported.
func NewCommon(
	log *logrus.Logger,
	tr *i18n.TranslationSet,
	cfg *config.UserConfig,
	appState *AppState,
	fs afero.Fs,
) *Common {
	if cfg == nil {
		panic("NewCommon: cfg is nil")
	}
	c := &Common{
		Log:      log,
		Tr:       tr,
		AppState: appState,
		Fs:       fs,
	}
	c.UserConfig.Store(cfg)
	if goos := getGOOS(); goos != "linux" && goos != "darwin" {
		if log != nil {
			log.Warnf("OS not officially supported: %s (continuing on best-effort)", goos)
		}
	}
	return c
}

// Cfg returns the current live config pointer. This is the canonical accessor
// (D-UC1) — callers must use it at each point of use rather than caching the
// returned *config.UserConfig across hot-reload boundaries.
func (c *Common) Cfg() *config.UserConfig { return c.UserConfig.Load() }
