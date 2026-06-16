package app

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/minio/selfupdate"
	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/env"
	"github.com/davesavic/pgsavvy/pkg/logs"
	"github.com/davesavic/pgsavvy/pkg/update"
)

// repoOwner / repoName identify the GitHub repository the updater pulls
// releases from. They mirror the goreleaser/CI publish target.
const (
	repoOwner = "davesavic"
	repoName  = "pgsavvy"
)

// updateLockName is the exclusive lockfile under the state dir that serializes
// concurrent `pgsavvy update` invocations.
const updateLockName = "update.lock"

// managedPrefixes are install roots owned by a package manager. A binary
// resolved under one of these must be updated via that manager, never by a
// self-replace (which would silently corrupt the managed install — e.g. a brew
// Cellar dir is user-writable so CheckPermissions alone would not catch it).
var managedPrefixes = []string{"/opt/homebrew", "/usr/local/Cellar", "/nix/store"}

// runUpdate handles the `pgsavvy update` subcommand. It runs entirely before
// the TUI/alt-screen and before the session logger, so os.Stdout/os.Stderr are
// the user's only live surface. Errors returned here propagate to main.go's
// log.Fatal (non-zero exit); user-facing detail is also written to Stdout.
func runUpdate(build *BuildInfo, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("pgsavvy update does not accept arguments")
	}

	// Acquire the exclusive lock FIRST, before any download/apply. State dir,
	// not the exec dir — the exec dir may be read-only, which is exactly when
	// the lock must still function.
	stateDir := env.GetStateDir()
	release, err := acquireUpdateLock(stateDir)
	if err != nil {
		return err
	}
	defer release()

	// Open a dedicated update log. NON-FATAL: never block an update on a
	// missing log dir. Mirrors the session-logger wiring minimally.
	log, logCloser := openUpdateLog(stateDir, build)
	if logCloser != nil {
		defer func() { _ = logCloser.Close() }()
	}

	result, err := update.Run(update.Options{
		CurrentVersion: build.Version,
		BuildSource:    build.BuildSource,
		RepoOwner:      repoOwner,
		RepoName:       repoName,
		GOOS:           runtime.GOOS,
		GOARCH:         runtime.GOARCH,
		Stdout:         os.Stdout,
	})
	if err != nil {
		return err
	}

	logs.Event(log, "update", "latest_tag_resolved",
		slog.String("current", result.CurrentVersion),
		slog.String("latest", result.LatestTag),
	)

	if result.UpToDate {
		fmt.Printf("pgsavvy %s is already the latest release.\n", result.CurrentVersion)
		return nil
	}

	logs.Event(log, "update", "asset_selected",
		slog.String("name", result.AssetName),
		slog.Int64("size", result.AssetSize),
		slog.String("url", result.AssetURL),
	)
	logs.Event(log, "update", "sha256_verified", slog.String("name", result.AssetName))

	fmt.Printf("Updating pgsavvy %s -> %s\n", result.CurrentVersion, result.LatestTag)
	fmt.Printf("  asset: %s\n", result.AssetURL)
	fmt.Printf("  downloading %s (%d bytes)\n", result.AssetName, result.AssetSize)

	return applyUpdate(result, log)
}

// applyUpdate resolves the running executable, refuses managed/read-only
// installs, and self-replaces using T2's verified bytes (NEVER re-fetching).
func applyUpdate(result *update.Result, log *slog.Logger) error {
	invoked, err := os.Executable()
	if err != nil {
		return fmt.Errorf("update: locate running executable: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(invoked)
	if err != nil {
		return fmt.Errorf("update: resolve executable symlinks: %w", err)
	}

	if msg := managedInstallReason(invoked, resolved); msg != "" {
		fmt.Fprintln(os.Stderr, msg)
		return fmt.Errorf("update: %s", msg)
	}

	opts := selfupdate.Options{
		TargetPath: resolved,
		Checksum:   result.Checksum, // raw 32-byte sha256 → Apply re-verifies the exact bytes
	}
	if err := opts.CheckPermissions(); err != nil {
		const msg = "managed or read-only install — update via your package manager"
		fmt.Fprintln(os.Stderr, msg)
		return fmt.Errorf("update: %s: %w", msg, err)
	}

	oldPath := filepath.Join(filepath.Dir(resolved), "."+filepath.Base(resolved)+".old")

	logs.Event(log, "update", "apply_start",
		slog.String("target", resolved),
		slog.String("old_backup", oldPath),
	)

	if err := selfupdate.Apply(bytes.NewReader(result.Verified), opts); err != nil {
		logs.Event(log, "update", "apply_result",
			slog.String("status", "error"),
			slog.String("err", err.Error()),
			slog.String("old_backup", oldPath),
		)
		if rerr := selfupdate.RollbackError(err); rerr != nil {
			fmt.Fprintf(os.Stderr,
				"update failed AND rollback failed: %v (rollback: %v).\n"+
					"Your binary may be missing. Recover it from the backup at %q or reinstall manually.\n",
				err, rerr, oldPath)
			return fmt.Errorf("update: apply failed, rollback failed: %w", err)
		}
		fmt.Fprintf(os.Stderr,
			"update failed: %v.\nThe previous binary was restored. A backup may remain at %q.\n",
			err, oldPath)
		return fmt.Errorf("update: apply failed: %w", err)
	}

	logs.Event(log, "update", "apply_result",
		slog.String("status", "ok"),
		slog.String("old_backup", oldPath),
	)

	if runtime.GOOS == "windows" {
		fmt.Printf(
			"Updated pgsavvy to %s. Re-run pgsavvy to use the new version.\n"+
				"The previous executable was left alongside it as %q (Windows cannot delete a running .exe).\n",
			result.LatestTag, oldPath)
		return nil
	}

	fmt.Printf("Updated pgsavvy to %s. Re-run pgsavvy to use the new version.\n", result.LatestTag)
	return nil
}

// managedInstallReason returns a non-empty refusal message when the resolved
// executable is under a managed prefix, or when the resolved path differs from
// the invoked path (a symlink — e.g. brew links into a Cellar). Empty string
// means the install is safe to self-replace.
func managedInstallReason(invoked, resolved string) string {
	const msg = "managed or read-only install — update via your package manager"
	if invoked != resolved {
		return msg
	}
	for _, p := range managedPrefixes {
		if strings.HasPrefix(resolved, p+string(os.PathSeparator)) {
			return msg
		}
	}
	return ""
}

// acquireUpdateLock takes an exclusive, advisory lock under stateDir by creating
// updateLockName with O_CREATE|O_EXCL. A second concurrent `pgsavvy update`
// fails the exclusive create and is told an update is in progress. The returned
// release func removes the lockfile; it is safe to call on every exit path.
func acquireUpdateLock(stateDir string) (func(), error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("update: ensure state dir: %w", err)
	}
	lockPath := filepath.Join(stateDir, updateLockName)
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("update in progress (lock held at %q)", lockPath)
		}
		return nil, fmt.Errorf("update: acquire lock: %w", err)
	}
	_ = f.Close()
	return func() { _ = os.Remove(lockPath) }, nil
}

// openUpdateLog opens a dedicated update log under stateDir using the shared
// logs sink. Failure is NON-FATAL: a missing log dir must never block an
// update, so on error this returns a no-op nil logger (logs.Event tolerates it)
// and a nil closer.
func openUpdateLog(stateDir string, build *BuildInfo) (*slog.Logger, io.Closer) {
	log, closer, err := logs.Open(logs.Options{
		Dir:            stateDir,
		FS:             afero.NewOsFs(),
		RetentionCount: 20,
		Redactor:       logs.DefaultRedactor(),
		BuildInfo: logs.BuildInfo{
			Version: build.Version,
			Commit:  build.Commit,
			Date:    build.Date,
		},
	})
	if err != nil {
		return nil, nil
	}
	return log, closer
}
