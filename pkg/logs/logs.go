package logs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sirupsen/logrus"

	"github.com/davesavic/dbsavvy/pkg/constants"
	"github.com/davesavic/dbsavvy/pkg/env"
)

// Init opens the log file at $XDG_STATE_HOME/dbsavvy/dbsavvy.log with mode
// 0600 (parent dir 0700). Single-instance safety only in v1: concurrent
// processes writing to the same file may interleave large lines (>PIPE_BUF).
// Multi-process safety is deferred to E12 (D-LOGS1). On HOME-unset hosts
// (e.g., minimal Docker containers), returns a non-nil error.
func Init() (*logrus.Logger, error) {
	stateDir := env.GetStateDir()
	if stateDir == "" || stateDir == constants.XDGAppDir {
		return nil, errors.New("logs: state dir is empty; HOME may be unset")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("logs: mkdir state dir: %w", err)
	}
	logPath := filepath.Join(stateDir, constants.DefaultLogFile)
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("logs: open log file: %w", err)
	}
	logger := logrus.New()
	logger.SetOutput(f)
	logger.SetFormatter(&logrus.TextFormatter{DisableColors: true, FullTimestamp: true})
	return logger, nil
}
