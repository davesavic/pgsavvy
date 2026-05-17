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
	"github.com/davesavic/dbsavvy/pkg/models"
)

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
	log := logrus.New()
	log.SetLevel(logrus.WarnLevel)

	c := common.NewCommon(log, tr, cfg, &common.AppState{}, fs)

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
