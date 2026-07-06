package app

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	"github.com/davesavic/pgsavvy/pkg/env"
	"github.com/davesavic/pgsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/logs"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/theme"
)

// disableSessionLogEnv is the kill switch (SC#8). When set to "1", the app
// reverts to the pre-feature stderr-only WarnLevel logger with no file and no
// redaction hook — for emergency rollback when the session log itself causes
// problems.
const disableSessionLogEnv = "PGSAVVY_DISABLE_SESSION_LOG"

// logDirEnv overrides the directory that holds the per-session log file's
// sessions/ subdir. Precedence: --log-dir flag > PGSAVVY_LOG_DIR > state dir.
const logDirEnv = "PGSAVVY_LOG_DIR"

// BuildInfo carries build-time metadata injected via -ldflags.
type BuildInfo struct {
	Commit      string
	Date        string
	Version     string
	BuildSource string
}

// Start is the CLI entry point. It parses --version (the only flag in
// v1), seeds the global *common.Common, builds the AppStateStore +
// orchestrator, installs a signal handler that asks the gocui MainLoop
// to quit cleanly, and runs the TUI. The M15c shutdown order
// (store.Flush → store.Close → driver.Close) is enforced by
// orchestrator.Gui.Close.
func Start(build *BuildInfo, args []string) error {
	// Route the `update` subcommand BEFORE flag parsing and before
	// orchestrator.NewGui — self-update never runs inside the gocui alt-screen.
	// args is os.Args[1:], so args[0]=="update" cannot collide with the
	// --version/--log-dir flags or the bare-TUI path.
	if len(args) > 0 && args[0] == "update" {
		return runUpdate(build, args[1:])
	}

	flags := flag.NewFlagSet("pgsavvy", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	showVersion := flags.Bool("version", false, "print version and exit")
	logDirFlag := flags.String("log-dir", "",
		"directory for per-session log files (overrides $PGSAVVY_LOG_DIR and $XDG_STATE_HOME/pgsavvy); logs land in <dir>/sessions/")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		fmt.Printf("pgsavvy %s (%s)\n", build.Version, build.BuildSource)
		return nil
	}

	configDir := env.GetConfigDir()
	stateDir := env.GetStateDir()
	fs := afero.NewOsFs()

	logDir, logDirOverridden := resolveLogDir(*logDirFlag, os.Getenv(logDirEnv), stateDir)

	if err := config.EnsureInitialConfig(fs, configDir); err != nil {
		return fmt.Errorf("app: ensure config: %w", err)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("app: ensure state dir: %w", err)
	}

	connectionsPath := filepath.Join(configDir, "connections.yml")
	queriesPath := filepath.Join(configDir, "queries.yml")
	statePath := filepath.Join(stateDir, "state.yml")
	configPath := filepath.Join(configDir, "config.yml")

	cfg, err := config.LoadUserConfig(fs, []string{configPath})
	if err != nil {
		return fmt.Errorf("app: load config: %w", err)
	}

	// require a key-driven exit BEFORE gocui.NewGui
	// puts us on the alt-screen. A config that REPLACES keybindings wholesale
	// (no element merge) can drop the default app.quit binding, leaving the
	// app un-quittable. Validating after NewGui is useless: the stderr message
	// would be wiped by tcell's Fini at g.Close. Returning here surfaces the
	// error via main.go's log.Fatal AFTER the terminal is restored.
	if err := requireQuitBinding(cfg, configPath); err != nil {
		return err
	}

	store := common.NewAppStateStore(fs, statePath, common.DefaultClock())
	_ = store.Load() // missing state file → defaults; not an error.

	log, logCloser, err := wireSessionLogger(logDir, logDirOverridden, fs, build)
	if err != nil {
		return err
	}

	// detect host locale and merge any overlay JSON over the
	// English baseline. LoadAndMerge's contract guarantees a non-nil
	// *TranslationSet even on error and only returns non-nil error in soft
	// cases; we Warn-log and continue with whatever set LoadAndMerge returns.
	lang := i18n.DetectLocale().String()
	tr, lerr := i18n.LoadAndMerge(nil, lang, log)
	if lerr != nil {
		log.Warn("i18n: LoadAndMerge failed; using returned set", "err", lerr, "cat", "app")
	}
	log.Info("i18n: bootstrap", "lang", lang, "cat", "app")

	c := common.NewCommon(log, tr, cfg, &common.AppState{}, fs)
	c.StateDir = stateDir
	c.LogCloser = logCloser
	// wire the per-session logger into the store so
	// MutateAndSave / debouncedFire / Close emit cat=state events.
	store.SetLogger(log)
	// hand the per-session logger to the pg driver package
	// so Driver.Open / Connection.Cancel / Session lifecycle emits land in
	// the per-session log file. Invoked AFTER logs.Open and BEFORE
	// g.RunAndHandleError() (AD-11 — preserves the init-time
	// drivers.Register invariant; the registration in main.go runs before
	// logs.Open has been called).
	pg.SetGlobalLogger(log)

	// Validate + apply the user's theme so config.yml colours take effect on
	// the first frame. theme.Current() is read only at render time (after
	// gocui.NewGui below), so applying here overrides the DefaultDark set by
	// theme's package init() in time for the first frame. Warnings for
	// unrenderable tokens go to stderr before gocui takes the screen, matching
	// the config-warning surface in wire_backbone.go. NOTE: the :reload
	// ex-command does NOT re-apply the theme — an on-disk reload is a separate
	// follow-up (epic pgsavvy-w1gh).
	applyUserTheme(cfg, os.Stderr)

	connectionsProvider := func() []models.Connection {
		conns, _ := config.LoadConnections(fs, connectionsPath)
		return conns
	}

	g := orchestrator.NewGui(orchestrator.Deps{
		Common:              c,
		Store:               store,
		ConnectionsPath:     connectionsPath,
		QueriesPath:         queriesPath,
		UserConfigPath:      configPath,
		ConnectionsProvider: connectionsProvider,
		DriverNamesFn:       drivers.Names,
		SetSecretPrompter:   pg.SetSecretPrompter,
		SetPasswordPrompter: pg.SetPasswordPrompter,
	})

	// Signal handler asks the MainLoop to quit (M15c: never call Flush
	// directly from the signal goroutine — let MainLoop unwind, then
	// the deferred Close runs Flush + Close + Close in order).
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		g.QuitOnSignal()
	}()
	defer signal.Stop(sigCh)
	defer func() { _ = g.Close() }()

	return g.RunAndHandleError()
}

// requireQuitBinding returns a hard, actionable error when cfg has no
// keybinding bound to config.QuitAction (app.quit) — i.e. no key-driven
// exit. The message NAMES the missing action and POINTS at configPath so a
// locked-out user can fix or remove the file. Returns
// nil when a quit binding is present.
func requireQuitBinding(cfg *config.UserConfig, configPath string) error {
	if config.HasQuitBinding(cfg) {
		return nil
	}
	return fmt.Errorf(
		"app: config %q has no %q keybinding; the app would have no key-driven exit. "+
			"Add a keybinding with action %q (e.g. key \"<c-c>\" or \"<leader>q\") to %q, or remove the file to restore defaults: %w",
		configPath, config.QuitAction, config.QuitAction, configPath, config.ErrNoQuitBinding,
	)
}

// resolveLogDir applies the precedence flag > env > stateDir. Empty / whitespace
// values fall through to the next-lower precedence. Returns the chosen dir and
// whether an explicit override (flag or env) was applied — callers use the
// override flag to fail fast on mkdir errors instead of silently falling back.
func resolveLogDir(flagVal, envVal, stateDir string) (string, bool) {
	if v := strings.TrimSpace(flagVal); v != "" {
		return v, true
	}
	if v := strings.TrimSpace(envVal); v != "" {
		return v, true
	}
	return stateDir, false
}

// applyUserTheme validates and applies the user's theme config, writing one
// `config: warning: ...` line to w for each unrenderable colour token (zero
// warnings writes nothing). It is the testable seam for the theme-wiring step:
// production passes os.Stderr; tests pass a *bytes.Buffer to exercise the
// apply+print path without launching gocui. The apply mutates theme.Current(),
// which is read only at render time, so calling this before gocui.NewGui makes
// the configured colours live for the first frame.
func applyUserTheme(cfg *config.UserConfig, w io.Writer) {
	for _, warn := range theme.ApplyUserConfig(&cfg.Theme) {
		// Best-effort stderr surface; a failed warning write must not abort
		// startup. w is a generic io.Writer (os.Stderr in prod) so errcheck
		// cannot apply its stderr exemption here.
		_, _ = fmt.Fprintf(w, "config: warning: %s\n", warn)
	}
}

// wireSessionLogger builds the primary logger. Behavior:
//   - PGSAVVY_DISABLE_SESSION_LOG=1 → stderr-only WarnLevel slog logger,
//     no file, but RedactingHandler is still wired (AMD-F2-6 closes the
//     pre-migration gap where the kill-switch path leaked unredacted DSNs).
//   - logs.Open success → DEBUG-level file logger + stderr Warn+ via the
//     handler chain Open() builds.
//   - logs.Open failure with no override → fallback stderr slog WITH
//     RedactingHandler over a JSON sink at WarnLevel + a single Warn line
//     documenting the failure. App still starts.
//   - logs.Open failure with override (flag or env) → return error so the
//     caller surfaces it. Operators who chose an explicit path want to know
//     it failed, not to silently get a stderr fallback.
func wireSessionLogger(logDir string, overridden bool, fs afero.Fs, build *BuildInfo) (*slog.Logger, io.Closer, error) {
	if os.Getenv(disableSessionLogEnv) == "1" {
		return newKillSwitchLogger(), nil, nil
	}

	logger, closer, err := logs.Open(logs.Options{
		Dir:            logDir,
		FS:             fs,
		RetentionCount: 20,
		Redactor:       logs.DefaultRedactor(),
		BuildInfo: logs.BuildInfo{
			Version: build.Version,
			Commit:  build.Commit,
			Date:    build.Date,
		},
	})
	if err != nil {
		if overridden {
			return nil, nil, fmt.Errorf("app: open session log at %q: %w", logDir, err)
		}
		fb := newFallbackLogger()
		fb.Warn("logs: session logger open failed; using stderr fallback", "err", err)
		return fb, nil, nil
	}
	return logger, closer, nil
}

// newFallbackLogger builds a stderr Warn+ slog wrapped in
// RedactingHandler (AD-13d / AMD-F2-6). Used when logs.Open fails.
func newFallbackLogger() *slog.Logger {
	base := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})
	return slog.New(logs.NewRedactingHandler(base, logs.DefaultRedactor()))
}

// newKillSwitchLogger builds the PGSAVVY_DISABLE_SESSION_LOG=1 logger:
// stderr text output at WarnLevel, RedactingHandler retained so that the
// emergency-rollback path does not leak credentials (AMD-F2-6).
func newKillSwitchLogger() *slog.Logger {
	base := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})
	return slog.New(logs.NewRedactingHandler(base, logs.DefaultRedactor()))
}
