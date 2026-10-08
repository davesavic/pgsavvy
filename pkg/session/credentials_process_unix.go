//go:build unix

package session

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// The supervisor retains the group leader until its output is drained or
// cancellation cleanup finishes. Even an unexpectedly exited, unreaped leader
// pins its PID, so group cancellation cannot target a recycled process group.
const passwordSupervisor = `"$@" </dev/null 3<&- 4>&-
_pgsavvy_password_exit=$?
exec 1>&- 2>&-
read -r _pgsavvy_password_release <&3 || :
exit "$_pgsavvy_password_exit"`

func runPasswordCommand(ctx context.Context, c *exec.Cmd) error {
	outR, outW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() { _ = outR.Close(); _ = outW.Close() }()
	errR, errW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() { _ = errR.Close(); _ = errW.Close() }()
	releaseR, releaseW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() { _ = releaseR.Close(); _ = releaseW.Close() }()
	lifetimeR, lifetimeW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() { _ = lifetimeR.Close(); _ = lifetimeW.Close() }()

	stdout, stderr := c.Stdout, c.Stderr
	originalArgs := append([]string{c.Path}, c.Args[1:]...)
	// The selected shell need not understand the supervisor's POSIX syntax.
	c.Path = "/bin/sh"
	c.Args = append([]string{c.Path, "-c", passwordSupervisor, "pgsavvy-password-supervisor"}, originalArgs...)
	c.Stdout, c.Stderr = outW, errW
	c.ExtraFiles = []*os.File{releaseR, lifetimeW}
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Only this function cancels the group, and it stops doing so before Wait
	// reaps the leader. CommandContext.Start still rejects precanceled contexts.
	c.Cancel = nil
	c.WaitDelay = 0
	if err := c.Start(); err != nil {
		return err
	}
	_ = outW.Close()
	_ = errW.Close()
	_ = releaseR.Close()
	_ = lifetimeW.Close()

	drained := make(chan error, 2)
	go func() { _, err := io.Copy(stdout, outR); drained <- err }()
	go func() { _, err := io.Copy(stderr, errR); drained <- err }()
	// Only the supervisor retains FD4. Its EOF observes actual exit, including
	// startup-defined exit traps, without reaping/releasing the group identity.
	exited := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, lifetimeR); exited <- err }()
	ctxDone := ctx.Done()
	var drainTimer *time.Timer
	var drainDeadline <-chan time.Time
	var copyErr, killErr error
	canceled := false
	cancelGroup := func() {
		canceled = true
		ctxDone = nil
		killErr = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		if errors.Is(killErr, syscall.ESRCH) {
			killErr = nil
		} else if killErr != nil {
			_ = c.Process.Kill()
		}
		drainTimer = time.NewTimer(250 * time.Millisecond)
		drainDeadline = drainTimer.C
	}
	leaderExited := false
	for remaining := 2; remaining > 0 || !leaderExited; {
		select {
		case err := <-drained:
			remaining--
			copyErr = errors.Join(copyErr, err)
			if remaining == 0 {
				_ = releaseW.Close()
			}
		case err := <-exited:
			leaderExited = true
			copyErr = errors.Join(copyErr, err)
		case <-ctxDone:
			cancelGroup()
		case <-drainDeadline:
			_ = outR.Close()
			_ = errR.Close()
			_ = lifetimeR.Close()
			drainDeadline = nil
		}
	}
	if !canceled && ctx.Err() != nil {
		cancelGroup()
	}
	if drainTimer != nil {
		drainTimer.Stop()
	}
	_ = releaseW.Close()
	waitErr := c.Wait()
	if canceled {
		return errors.Join(ctx.Err(), killErr)
	}
	if waitErr != nil {
		return waitErr
	}
	return copyErr
}
