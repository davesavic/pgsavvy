package common

import (
	"io"
	"log/slog"
	"runtime"
	"sync/atomic"

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
//
// Exception: LogCloser is a field-only addition authorized by epic
// dbsavvy-8s2 (AD-18) for per-session log file teardown in M15c.
//
// The structured logger is held in the private `log` field and accessed via
// Logger(); see AMD-F2-1 (dbsavvy-962). Logger() is nil-safe by construction,
// returning slog.New(slog.DiscardHandler) when c or c.log is nil, so consumer
// code must NOT do its own nil checks.
type Common struct {
	log *slog.Logger
	Tr  *i18n.TranslationSet
	// UserConfig holds the live config pointer. Always call Common.Cfg() at point
	// of use. Never cache the *config.UserConfig across hot-reload boundaries —
	// the pointer is swapped on reload. See DESIGN.md §15.3.
	UserConfig atomic.Pointer[config.UserConfig]
	AppState   *AppState
	Fs         afero.Fs
	// StateDir is the per-user state directory rooted at env.GetStateDir().
	// Populated by entry_point.Start after NewCommon. Empty in tests that
	// don't exercise persistence (dbsavvy-wwd.9).
	StateDir string
	// LogCloser closes the per-session log file during M15c shutdown.
	// Field-only addition (AD-18); NewCommon does not accept it.
	// entry_point.Start sets it after NewCommon, mirroring StateDir.
	// nil-tolerant — tests that build a *Common directly leave it nil.
	LogCloser io.Closer
}

// NewCommon constructs a *Common wired to the supplied dependencies. It
// panics with "NewCommon: cfg is nil" if cfg is nil, and with
// "NewCommon: log is nil" if log is nil — both pointers are non-optional
// invariants of the bag (AD-A4). The cfg check runs first; callers that
// pass both nil see the cfg panic. Before returning, the supplied cfg is
// published via UserConfig.Store so that c.Cfg() is immediately safe to
// call. If the runtime OS is not in {"linux","darwin"} a warning is logged
// to flag that the platform is not officially supported.
func NewCommon(
	log *slog.Logger,
	tr *i18n.TranslationSet,
	cfg *config.UserConfig,
	appState *AppState,
	fs afero.Fs,
) *Common {
	if cfg == nil {
		panic("NewCommon: cfg is nil")
	}
	if log == nil {
		panic("NewCommon: log is nil")
	}
	c := &Common{
		log:      log,
		Tr:       tr,
		AppState: appState,
		Fs:       fs,
	}
	c.UserConfig.Store(cfg)
	if goos := getGOOS(); goos != "linux" && goos != "darwin" {
		log.Warn("OS not officially supported (continuing on best-effort)", "os", goos)
	}
	return c
}

// Logger returns the structured logger associated with this Common. It is
// nil-safe: when c is nil or c.log is nil, a discarding logger is returned so
// callers never need to gate emits. Production wiring always supplies a real
// logger via NewCommon (which panics on nil), so the discard fallback only
// triggers in tests that build *Common via struct-literal without a log.
func (c *Common) Logger() *slog.Logger {
	if c == nil || c.log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return c.log
}

// Cfg returns the current live config pointer. This is the canonical accessor
// (D-UC1) — callers must use it at each point of use rather than caching the
// returned *config.UserConfig across hot-reload boundaries.
func (c *Common) Cfg() *config.UserConfig { return c.UserConfig.Load() }
