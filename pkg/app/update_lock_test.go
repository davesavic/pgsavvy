package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReviewUpdateLockHelper(t *testing.T) {
	mode := os.Getenv("PGSAVVY_LOCK_TEST_MODE")
	if mode == "" {
		return
	}
	dir := os.Getenv("PGSAVVY_LOCK_TEST_DIR")
	var release func()
	var err error
	if mode == "legacy" {
		path := filepath.Join(dir, updateLockName)
		var f *os.File
		f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = f.Close()
			release = func() { _ = os.Remove(path) }
		}
	} else {
		stage := os.Getenv("PGSAVVY_LOCK_TEST_STAGE")
		if stage == "" {
			release, err = acquireUpdateLock(dir)
		} else {
			release, err = acquireUpdateLockWithHook(dir, func(current string) {
				if current == stage {
					fmt.Println(current)
					_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
				}
			})
		}
	}
	if err != nil {
		fmt.Println("error: " + err.Error())
		return
	}
	defer release()
	fmt.Println("ready")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

type updateLockProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Scanner
	waited bool
}

func startUpdateLockProcess(t *testing.T, dir, mode string, stage ...string) (*updateLockProcess, string) {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestReviewUpdateLockHelper$")
	cmd.Env = append(os.Environ(), "PGSAVVY_LOCK_TEST_MODE="+mode, "PGSAVVY_LOCK_TEST_DIR="+dir)
	if len(stage) > 0 {
		cmd.Env = append(cmd.Env, "PGSAVVY_LOCK_TEST_STAGE="+stage[0])
	}
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())
	p := &updateLockProcess{cmd: cmd, stdin: stdin, stdout: bufio.NewScanner(stdout)}
	t.Cleanup(func() {
		_ = stdin.Close()
		if !p.waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	return p, p.nextLine(t)
}

func (p *updateLockProcess) nextLine(t *testing.T) string {
	t.Helper()
	require.True(t, p.stdout.Scan(), "helper must report readiness or refusal")
	return p.stdout.Text()
}

func (p *updateLockProcess) resume(t *testing.T) {
	t.Helper()
	_, err := io.WriteString(p.stdin, "\n")
	require.NoError(t, err)
}

func (p *updateLockProcess) wait(t *testing.T) {
	t.Helper()
	require.NoError(t, p.cmd.Wait())
	p.waited = true
}

func (p *updateLockProcess) kill(t *testing.T) {
	t.Helper()
	require.NoError(t, p.cmd.Process.Kill())
	require.Error(t, p.cmd.Wait())
	p.waited = true
}

func (p *updateLockProcess) finish(t *testing.T) {
	t.Helper()
	p.resume(t)
	p.wait(t)
}

func TestReview_update_lock_process_recovery(t *testing.T) {
	dir := t.TempDir()
	owner, ready := startUpdateLockProcess(t, dir, "new")
	require.Equal(t, "ready", ready)
	contender, refused := startUpdateLockProcess(t, dir, "new")
	require.True(t, strings.Contains(refused, "update in progress"), refused)
	contender.wait(t)
	path := filepath.Join(dir, updateLockName)
	before, err := os.Stat(path)
	require.NoError(t, err)
	owner.kill(t)
	recovered, ready := startUpdateLockProcess(t, dir, "new")
	require.Equal(t, "ready", ready, "a dead owner's stable marker must be recoverable")
	after, err := os.Stat(path)
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after), "recovery must not replace the inode")
	recovered.finish(t)
}

func TestReview_update_lock_legacy_ordering(t *testing.T) {
	t.Run("legacy first", func(t *testing.T) {
		dir := t.TempDir()
		owner, ready := startUpdateLockProcess(t, dir, "legacy")
		require.Equal(t, "ready", ready)
		contender, refused := startUpdateLockProcess(t, dir, "new")
		require.Contains(t, refused, "legacy or unrecognized")
		contender.wait(t)
		owner.finish(t)
		owner, ready = startUpdateLockProcess(t, dir, "new")
		require.Equal(t, "ready", ready)
		owner.finish(t)
	})
	t.Run("new first", func(t *testing.T) {
		dir := t.TempDir()
		owner, ready := startUpdateLockProcess(t, dir, "new")
		require.Equal(t, "ready", ready)
		contender, refused := startUpdateLockProcess(t, dir, "legacy")
		require.Contains(t, refused, "error:")
		contender.wait(t)
		owner.finish(t)
		contender, refused = startUpdateLockProcess(t, dir, "legacy")
		require.Contains(t, refused, "error:", "ordinary release must keep excluding older binaries")
		contender.wait(t)
	})
}

func TestReview_update_lock_bootstrap_crashes(t *testing.T) {
	for _, stage := range []string{"temp_created", "marker_written", "published"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			owner, current := startUpdateLockProcess(t, dir, "new", stage)
			require.Equal(t, stage, current)
			path := filepath.Join(dir, updateLockName)
			var before os.FileInfo
			if stage == "published" {
				content, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, updateLockMarker, string(content))
				before, err = os.Stat(path)
				require.NoError(t, err)
				contender, refused := startUpdateLockProcess(t, dir, "new")
				require.Contains(t, refused, "update in progress")
				contender.wait(t)
			} else {
				_, err := os.Stat(path)
				require.True(t, os.IsNotExist(err), "bootstrap must not expose a stable marker before publication")
				candidates, err := filepath.Glob(filepath.Join(dir, ".update-lock-*"))
				require.NoError(t, err)
				require.Len(t, candidates, 1)
				if stage == "temp_created" {
					// Simulate interruption during a write, while publication is
					// deterministically paused and no stable marker is visible.
					require.NoError(t, os.WriteFile(candidates[0], []byte("pgsavvy-"), 0o600))
				}
			}
			owner.kill(t)
			recovered, ready := startUpdateLockProcess(t, dir, "new")
			require.Equal(t, "ready", ready)
			content, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, updateLockMarker, string(content))
			if before != nil {
				after, err := os.Stat(path)
				require.NoError(t, err)
				require.True(t, os.SameFile(before, after))
			}
			recovered.finish(t)
		})
	}
}

func TestReview_update_lock_bootstrap_contenders(t *testing.T) {
	for _, mode := range []string{"legacy", "new"} {
		t.Run(mode+" wins publication", func(t *testing.T) {
			dir := t.TempDir()
			publisher, stage := startUpdateLockProcess(t, dir, "new", "marker_written")
			require.Equal(t, "marker_written", stage)
			winner, ready := startUpdateLockProcess(t, dir, mode)
			require.Equal(t, "ready", ready)
			path := filepath.Join(dir, updateLockName)
			before, err := os.Stat(path)
			require.NoError(t, err)
			content, err := os.ReadFile(path)
			require.NoError(t, err)
			publisher.resume(t)
			refused := publisher.nextLine(t)
			if mode == "legacy" {
				require.Contains(t, refused, "legacy or unrecognized")
			} else {
				require.Contains(t, refused, "update in progress")
			}
			publisher.wait(t)
			after, err := os.Stat(path)
			require.NoError(t, err)
			require.True(t, os.SameFile(before, after))
			afterContent, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, content, afterContent)
			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			require.Len(t, entries, 1, "losing publisher must remove only its private candidate")
			winner.finish(t)
		})
	}
}

func TestReview_update_lock_io_errors(t *testing.T) {
	t.Run("state path is a file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state")
		require.NoError(t, os.WriteFile(path, []byte("unchanged"), 0o600))
		release, err := acquireUpdateLock(path)
		require.Nil(t, release)
		require.ErrorContains(t, err, "ensure state dir")
		content, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, "unchanged", string(content))
	})
	t.Run("publication target is a directory", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, updateLockName)
		require.NoError(t, os.Mkdir(path, 0o700))
		f, err := publishUpdateLock(path, nil)
		require.Nil(t, f)
		require.ErrorContains(t, err, "publish initialized lock without overwrite")
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		require.True(t, entries[0].IsDir())
	})
	t.Run("stable marker is a symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target")
		require.NoError(t, os.WriteFile(target, []byte(updateLockMarker), 0o600))
		path := filepath.Join(dir, updateLockName)
		if err := os.Symlink(target, path); err != nil {
			if runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(1314)) { // ERROR_PRIVILEGE_NOT_HELD
				t.Skipf("Windows symlink creation requires privilege: %v", err)
			}
			require.NoError(t, err)
		}
		release, err := acquireUpdateLock(dir)
		require.Nil(t, release)
		require.Error(t, err)
		link, err := os.Readlink(path)
		require.NoError(t, err)
		require.Equal(t, target, link)
		content, err := os.ReadFile(target)
		require.NoError(t, err)
		require.Equal(t, updateLockMarker, string(content))
	})
}
