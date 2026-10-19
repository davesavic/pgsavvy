//go:build linux

package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/spf13/afero"
	"golang.org/x/sys/unix"

	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/env"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/query"
)

const cliHistoryBudget = 250 * time.Millisecond

type queryHistoryAttempt struct {
	event     query.HistoryEvent
	started   time.Time
	remaining time.Duration
	path      string
	append    func(context.Context, string, query.HistoryEvent) error
	finalized bool
}

func (s InvocationService) beginQueryHistory(ctx context.Context, name, sql string, started time.Time) (*queryHistoryAttempt, *models.Connection) {
	if name == "" || len(name) > MaxProfileBytes || !utf8.ValidString(name) {
		return nil, nil
	}
	lookup := s.historyLookup
	if lookup == nil {
		if s.Load != nil {
			// Injected execution loaders alone do not establish consent trust,
			// and must never accidentally consult a real user's configuration.
			return nil, nil
		}
		lookup = func(ctx context.Context, name string) (models.Connection, error) {
			return historyProfileMetadata(ctx, filepath.Join(env.GetConfigDir(), "connections.yml"), name)
		}
	}
	lookupStart := time.Now()
	lookupCtx, cancel := context.WithTimeout(ctx, cliHistoryBudget)
	profile, err := lookup(lookupCtx, name)
	cancel()
	remaining := cliHistoryBudget - time.Since(lookupStart)
	if err != nil {
		return nil, nil
	}
	profile, err = ResolveExactProfile([]models.Connection{profile}, name)
	if err != nil {
		return nil, nil
	}
	if !profile.CLIHistory {
		return nil, &profile
	}
	appendEvent := s.historyAppend
	if appendEvent == nil {
		appendEvent = query.AppendHistory
	}
	path := s.historyPath
	if path == "" {
		path = filepath.Join(env.GetStateDir(), "history.sqlite")
	}
	return &queryHistoryAttempt{
		event:   query.HistoryEvent{SQL: sql, ConnectionID: profile.Name, Source: "cli"},
		started: started, remaining: remaining, path: path, append: appendEvent,
	}, &profile
}

func (a *queryHistoryAttempt) complete(ctx context.Context, err error) {
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	a.event.Outcome = "succeeded"
	if err != nil {
		a.event.Outcome = "failed"
		if code := safeError(err).Code; code == UnsupportedSQL || code == PolicyDenied {
			a.event.Outcome = "blocked"
		}
	}
	now := time.Now()
	a.event.ExecutedAt = now.UnixMilli()
	a.event.DurationMS = now.Sub(a.started).Milliseconds()
}

func (a *queryHistoryAttempt) finalize(cutoff time.Time) []ErrorCode {
	if a.finalized {
		return nil
	}
	a.finalized = true
	deadline := time.Now().Add(max(0, a.remaining))
	if !cutoff.IsZero() && cutoff.Before(deadline) {
		deadline = cutoff
	}
	if a.remaining <= 0 || !time.Now().Before(deadline) {
		return []ErrorCode{HistoryUnavailable}
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	start := time.Now()
	err := a.append(ctx, a.path, a.event)
	a.remaining -= time.Since(start)
	cancel()
	if errors.Is(err, query.ErrHistoryUpgradeRequired) {
		return []ErrorCode{HistoryUpgradeRequired}
	}
	if err != nil {
		return []ErrorCode{HistoryUnavailable}
	}
	return nil
}

func historyProfileMetadata(ctx context.Context, path, name string) (models.Connection, error) {
	if err := ctx.Err(); err != nil {
		return models.Connection{}, err
	}
	// Resolve permitted directory aliases, then reject replaceable canonical
	// ancestors. The final file itself may not be a link or writable by others.
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return models.Connection{}, err
	}
	for parent := dir; ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil || !info.IsDir() {
			return models.Connection{}, os.ErrPermission
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || (stat.Uid != 0 && stat.Uid != uint32(os.Geteuid())) || (info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0) {
			return models.Connection{}, os.ErrPermission
		}
		if filepath.Dir(parent) == parent {
			break
		}
	}
	file, err := os.OpenFile(filepath.Join(dir, filepath.Base(path)), machineFileOpenFlags()|unix.O_NOFOLLOW, 0)
	if err != nil {
		return models.Connection{}, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
		return models.Connection{}, os.ErrPermission
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 || (stat.Uid != 0 && stat.Uid != uint32(os.Geteuid())) {
		return models.Connection{}, os.ErrPermission
	}
	data, err := readMachineInput(ctx, "-", file)
	if err != nil {
		return models.Connection{}, err
	}
	// Strict existing parsing on a memory-only snapshot: no profile lock,
	// credential helper, driver parsing, repair, or network operation.
	fs := afero.NewMemMapFs()
	if err := afero.WriteFile(fs, "/connections.yml", data, 0o600); err != nil {
		return models.Connection{}, err
	}
	profiles, err := config.LoadConnectionsContext(ctx, fs, "/connections.yml", io.Discard)
	if err != nil {
		return models.Connection{}, err
	}
	if ctx.Err() != nil {
		return models.Connection{}, ctx.Err()
	}
	return ResolveExactProfile(profiles, name)
}
