//go:build unix

package session

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func passwordProbePID(t *testing.T, dir, name string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err == nil {
			pid, err := strconv.Atoi(string(data))
			if err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("owned process readiness handshake timed out")
	return 0
}

func passwordProbeRunning(pid int) bool {
	if runtime.GOOS == "linux" {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return false
		}
		rest := string(data)[strings.LastIndexByte(string(data), ')')+1:]
		fields := strings.Fields(rest)
		return len(fields) > 0 && fields[0] != "Z"
	}
	return syscall.Kill(pid, 0) == nil
}

func passwordProbeStop(dir string) {
	_ = os.WriteFile(filepath.Join(dir, "stop"), nil, 0o600)
}

func passwordProbeAwaitStopped(t *testing.T, pid int, deadline time.Time) {
	t.Helper()
	if !passwordProbePollStopped(func() bool { return passwordProbeRunning(pid) }, deadline) {
		t.Error("owned process remains running at the cancellation deadline")
	}
}

func passwordProbePollStopped(running func() bool, deadline time.Time) bool {
	for {
		// A terminal observation ends polling; probing again can observe a
		// changing /proc entry and report failure before the deadline.
		if !running() {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPasswordProbePollStopped(t *testing.T) {
	t.Run("terminal_observation", func(t *testing.T) {
		calls := 0
		stopped := passwordProbePollStopped(func() bool {
			calls++
			return calls > 1
		}, time.Now().Add(time.Second))
		if !stopped || calls != 1 {
			t.Fatal("terminal observation was probed again instead of accepting process exit")
		}
	})
	t.Run("deadline", func(t *testing.T) {
		if passwordProbePollStopped(func() bool { return true }, time.Now()) {
			t.Fatal("running process accepted at deadline")
		}
	})
	t.Run("wait_for_exit", func(t *testing.T) {
		calls := 0
		if !passwordProbePollStopped(func() bool {
			calls++
			return calls == 1
		}, time.Now().Add(time.Second)) {
			t.Fatal("process exit before deadline was not accepted")
		}
	})
}

func TestReview_password_descendant_process(t *testing.T) {
	dir := os.Getenv("PGSAVVY_PASSWORD_PROBE_DIR")
	if dir == "" {
		return
	}
	signal.Ignore(syscall.SIGTERM)
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		os.Exit(2)
	}
	// Keep both inherited output pipes open until explicitly stopped. A hard
	// upper bound prevents an interrupted parent test from leaking a fixture.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "stop")); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestReview_password_closed_output_cancellation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SHELL", "/bin/sh")
	path := "'" + strings.ReplaceAll(filepath.Join(dir, "pid"), "'", "'\"'\"'") + "'"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := execPasswordCommand(ctx, "exec 1>&- 2>&-; printf '%s' \"$$\" > "+path+"; sleep 15")
		result <- err
	}()
	pid := passwordProbePID(t, dir, "pid")
	start := time.Now()
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("closed-output helper was not canceled and stopped")
		}
		passwordProbeAwaitStopped(t, pid, start.Add(time.Second))
	case <-time.After(time.Second):
		t.Fatal("closed-output helper cancellation exceeded one second")
	}
}

func TestReview_password_selected_shell(t *testing.T) {
	shell := filepath.Join(t.TempDir(), "selected-shell")
	const script = `#!/bin/sh
if [ "$1" != -c ] || [ "$2" != selected-shell-command ]; then
  exit 42
fi
printf result
`
	if err := os.WriteFile(shell, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", shell)
	got, err := execPasswordCommand(context.Background(), "selected-shell-command")
	if err != nil || got != "result" {
		t.Fatal("selected shell must receive only the original password command")
	}
}

func TestReview_password_shell_startup(t *testing.T) {
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("bash startup fixture unavailable")
	}
	t.Run("errexit", func(t *testing.T) {
		dir := t.TempDir()
		startup := filepath.Join(dir, "startup")
		if err := os.WriteFile(startup, []byte("set -e\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("SHELL", "/bin/bash")
		t.Setenv("BASH_ENV", startup)
		got, err := execPasswordCommand(context.Background(), "printf result")
		if err != nil || got != "result" {
			t.Fatal("inherited errexit changed successful helper status")
		}
	})
	t.Run("exit_trap_cancellation", func(t *testing.T) {
		dir := t.TempDir()
		startup := filepath.Join(dir, "startup")
		trap := `trap 'PGSAVVY_PASSWORD_PROBE_DIR="$PGSAVVY_PASSWORD_TRAP_DIR" "$PGSAVVY_PASSWORD_PROBE_BINARY" -test.run=^TestReview_password_descendant_process$' EXIT`
		if err := os.WriteFile(startup, []byte(trap+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("SHELL", "/bin/bash")
		t.Setenv("BASH_ENV", startup)
		t.Setenv("PGSAVVY_PASSWORD_TRAP_DIR", dir)
		t.Setenv("PGSAVVY_PASSWORD_PROBE_BINARY", os.Args[0])
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		result := make(chan error, 1)
		go func() { _, err := execPasswordCommand(ctx, "printf result"); result <- err }()
		t.Cleanup(func() { cancel(); passwordProbeStop(dir) })
		pid := passwordProbePID(t, dir, "pid")
		start := time.Now()
		cancel()
		select {
		case err := <-result:
			if err == nil {
				t.Fatal("helper exit trap lost cancellation classification")
			}
			passwordProbeAwaitStopped(t, pid, start.Add(time.Second))
		case <-time.After(time.Second):
			passwordProbeStop(dir)
			select {
			case <-result:
			case <-time.After(3 * time.Second):
				t.Fatal("supervisor fixture failed to stop")
			}
			t.Fatal("helper exit trap kept cancellation blocked beyond one second")
		}
	})
}

func TestReview_password_descendant_cancellation(t *testing.T) {
	sentinelDir := t.TempDir()
	sentinel := exec.Command(os.Args[0], "-test.run=^TestReview_password_descendant_process$")
	sentinel.Env = append(os.Environ(), "PGSAVVY_PASSWORD_PROBE_DIR="+sentinelDir)
	if err := sentinel.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { passwordProbeStop(sentinelDir); _ = sentinel.Wait() })
	sentinelPID := passwordProbePID(t, sentinelDir, "pid")
	for _, mode := range []string{"shell_alive", "shell_exited"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("PGSAVVY_PASSWORD_PROBE_DIR", dir)
			t.Setenv("SHELL", "/bin/sh")
			quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
			cmd := fmt.Sprintf("printf '%%s' \"$$\" > %s; %s -test.run=^TestReview_password_descendant_process$ & ", quote(filepath.Join(dir, "shellpid")), quote(os.Args[0]))
			if mode == "shell_alive" {
				cmd += "wait"
			} else {
				cmd += "exit 0"
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() { _, err := execPasswordCommand(ctx, cmd); result <- err }()
			t.Cleanup(func() {
				cancel()
				passwordProbeStop(dir)
			})
			pid := passwordProbePID(t, dir, "pid")
			if mode == "shell_exited" {
				shellPID := passwordProbePID(t, dir, "shellpid")
				deadline := time.Now().Add(2 * time.Second)
				for passwordProbeRunning(shellPID) && time.Now().Before(deadline) {
					time.Sleep(5 * time.Millisecond)
				}
				if passwordProbeRunning(shellPID) {
					t.Fatal("original shell did not exit before cancellation")
				}
			}
			start := time.Now()
			cancel()
			cancel()
			select {
			case err := <-result:
				if err == nil {
					t.Error("cancellation returned a password success")
				}
			case <-time.After(time.Second):
				passwordProbeStop(dir)
				select {
				case <-result:
				case <-time.After(3 * time.Second):
					t.Fatal("owned helper failed to stop after fixture release")
				}
				t.Fatal("pipe-holding descendant kept cancellation blocked beyond one second")
			}
			passwordProbeAwaitStopped(t, pid, start.Add(time.Second))
			if !passwordProbeRunning(sentinelPID) {
				t.Error("cancellation affected an independently owned process")
			}
			t.Logf("%s cancellation completed in %s; owned descendant stopped; independent sentinel alive", mode, time.Since(start))
		})
	}
}
