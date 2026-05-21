package app

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/sirupsen/logrus"
	"github.com/spf13/afero"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/env"
	"github.com/davesavic/dbsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/logs"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// disableSessionLogEnv is the kill switch (SC#8). When set to "1", the app
// reverts to the pre-feature stderr-only WarnLevel logger with no file and no
// redaction hook — for emergency rollback when the session log itself causes
// problems.
const disableSessionLogEnv = "DBSAVVY_DISABLE_SESSION_LOG"

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
	flags := flag.NewFlagSet("dbsavvy", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	showVersion := flags.Bool("version", false, "print version and exit")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		fmt.Printf("dbsavvy %s (%s)\n", build.Version, build.BuildSource)
		return nil
	}

	configDir := env.GetConfigDir()
	stateDir := env.GetStateDir()
	fs := afero.NewOsFs()

	if err := config.EnsureInitialConfig(fs, configDir); err != nil {
		return fmt.Errorf("app: ensure config: %w", err)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("app: ensure state dir: %w", err)
	}

	connectionsPath := filepath.Join(configDir, "connections.yml")
	statePath := filepath.Join(stateDir, "state.yml")
	configPath := filepath.Join(configDir, "config.yml")

	cfg, err := config.LoadUserConfig(fs, []string{configPath})
	if err != nil {
		return fmt.Errorf("app: load config: %w", err)
	}

	store := common.NewAppStateStore(fs, statePath, common.DefaultClock())
	_ = store.Load() // missing state file → defaults; not an error.

	tr := i18n.EnglishTranslationSet()
	log, logCloser := wireSessionLogger(stateDir, fs, build)

	c := common.NewCommon(log, tr, cfg, &common.AppState{}, fs)
	c.StateDir = stateDir
	c.LogCloser = logCloser
	// dbsavvy-8s2.7: wire the per-session logger into the store so
	// MutateAndSave / debouncedFire / Close emit cat=state events.
	store.SetLogger(log)

	connectionsProvider := func() []models.Connection {
		conns, _ := config.LoadConnections(fs, connectionsPath)
		return conns
	}

	g := orchestrator.NewGui(orchestrator.Deps{
		Common:              c,
		Store:               store,
		ConnectionsPath:     connectionsPath,
		ConnectionsProvider: connectionsProvider,
		DriverNamesFn:       drivers.Names,
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

// wireSessionLogger builds the primary logger. Behavior:
//   - DBSAVVY_DISABLE_SESSION_LOG=1 → pre-feature stderr-only WarnLevel logger
//     with no file and no redaction hook (SC#8 kill switch).
//   - logs.Open success → DEBUG-level file logger + stderr Warn+ + redaction hook.
//   - logs.Open failure → fallback stderr logger WITH redaction hook installed
//     (AD-13d) and a single Warn line documenting the failure. App still starts.
func wireSessionLogger(stateDir string, fs afero.Fs, build *BuildInfo) (*logrus.Logger, io.Closer) {
	if os.Getenv(disableSessionLogEnv) == "1" {
		log := logrus.New()
		log.SetLevel(logrus.WarnLevel)
		return log, nil
	}

	logger, closer, err := logs.Open(logs.Options{
		Dir:            stateDir,
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
		fb := newFallbackLogger()
		fb.WithError(err).Warn("logs: session logger open failed; using stderr fallback")
		return fb, nil
	}
	return logger, closer
}

// newFallbackLogger builds a stderr WarnLevel logrus with the default
// redactor hook installed (AD-13d). Used when logs.Open fails.
func newFallbackLogger() *logrus.Logger {
	log := logrus.New()
	log.SetLevel(logrus.WarnLevel)
	log.AddHook(logs.DefaultRedactor())
	return log
}
