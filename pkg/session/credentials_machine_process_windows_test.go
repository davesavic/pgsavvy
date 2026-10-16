package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

func machineDuplicateProcess(t *testing.T, handle windows.Handle) windows.Handle {
	t.Helper()
	var duplicate windows.Handle
	if windows.DuplicateHandle(windows.CurrentProcess(), handle, windows.CurrentProcess(), &duplicate, 0, false, windows.DUPLICATE_SAME_ACCESS) != nil {
		t.Fatal("owned process observation handle failed")
	}
	t.Cleanup(func() { _ = windows.CloseHandle(duplicate) })
	return duplicate
}

func machineAssertProcessStopped(t *testing.T, process windows.Handle) {
	t.Helper()
	if state, err := windows.WaitForSingleObject(process, 0); err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatal("an owned process remains alive")
	}
}

func machineHelperReady(t *testing.T, path string) windows.Handle {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.ParseUint(string(data), 10, 32)
			if parseErr == nil {
				process, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
				if err != nil {
					t.Fatal("test-owned descendant observation failed")
				}
				t.Cleanup(func() { _ = windows.CloseHandle(process) })
				return process
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("synthetic helper did not become ready")
		}
		time.Sleep(time.Millisecond)
	}
}

func machineWaitHelper(t *testing.T, done <-chan error, limit time.Duration) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(limit):
		t.Fatal("owned helper work did not join")
		return nil
	}
}

func machineRunHelperAsync(t *testing.T, ctx context.Context, cancel context.CancelFunc, command *exec.Cmd) <-chan error {
	t.Helper()
	done, joined := make(chan error, 1), make(chan struct{})
	go func() { defer close(joined); done <- runMachinePasswordCommand(ctx, command) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-joined:
		case <-time.After(2 * time.Second):
			t.Error("helper fixture cleanup failed to join")
		}
	})
	return done
}

func TestMachineWindowsHelper_Containment(t *testing.T) {
	for _, phase := range []string{"prelaunch", "suspended", "assigned", "deadline", "assignment-failed"} {
		t.Run(phase, func(t *testing.T) {
			var ctx context.Context
			var cancel context.CancelFunc
			if phase == "deadline" {
				ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
			} else {
				ctx, cancel = context.WithCancel(context.Background())
			}
			defer cancel()
			path := filepath.Join(t.TempDir(), "must-not-execute")
			command := machineCredentialHelperCommand(ctx, "touch", path)
			command.Stdout = io.Discard
			var process windows.Handle
			hooks := machineWindowsProcessHooks{suspended: func(handle windows.Handle) {
				process = machineDuplicateProcess(t, handle)
				if phase == "suspended" {
					cancel()
				}
			}}
			if phase == "assigned" {
				hooks.beforeResume = cancel
			}
			if phase == "deadline" {
				hooks.beforeResume = func() { <-ctx.Done() }
			}
			if phase == "prelaunch" {
				cancel()
			}
			if phase == "assignment-failed" {
				hooks.assign = func(_ windows.Handle, process windows.Handle) error {
					return windows.AssignProcessToJobObject(0, process)
				}
			}
			start := time.Now()
			err := runMachineWindowsPasswordCommand(ctx, command, hooks)
			if err == nil || time.Since(start) > 2*time.Second || strings.Contains(err.Error(), path) {
				t.Fatal("startup failure was unbounded, leaked, or succeeded")
			}
			want := error(context.Canceled)
			if phase == "deadline" {
				want = context.DeadlineExceeded
			}
			if phase != "assignment-failed" && !errors.Is(err, want) {
				t.Fatal("startup cancellation classification changed")
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("an uncontained or canceled child executed")
			}
			if process != 0 {
				machineAssertProcessStopped(t, process)
			}
		})
	}
	t.Run("restrictive-parent", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMachineCredentialHelperRestrictedParentProcess$")
		path := filepath.Join(t.TempDir(), "must-not-execute")
		command.Env = append(os.Environ(), "PGSAVVY_MACHINE_RESTRICTED_PARENT=1", "PGSAVVY_MACHINE_RESTRICTED_SENTINEL="+path)
		if err := command.Run(); err != nil {
			t.Fatal("restricted parent job fixture failed")
		}
	})
	for _, mode := range []string{"output", "stdin", "environment", "exit"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv(keyringPassphraseEnv, "synthetic-secret")
			t.Setenv("PGSAVVY_MACHINE_HELPER_VALUE", "retained")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			argument, want := "raw λ\r\n", "raw λ\r\n"
			if mode == "stdin" {
				want = "no-stdin"
			} else if mode == "environment" {
				want = "retained"
			} else if mode == "exit" {
				argument, want = "7", ""
			}
			command := machineCredentialHelperCommand(ctx, mode, argument)
			if mode == "environment" {
				command.Env = scrubbedEnv(append(command.Env,
					strings.ToLower(keyringPassphraseEnv)+"=synthetic-secret",
					"PGSAVVY_MACHINE_HELPER_VALUE=overwritten", "pgsavvy_machine_helper_value=retained"))
			}
			var output bytes.Buffer
			command.Stdout = &output
			err := runMachinePasswordCommand(ctx, command)
			if mode == "exit" {
				var status interface{ ExitCode() int }
				if !errors.As(err, &status) || status.ExitCode() != 7 {
					t.Fatal("helper exit status changed")
				}
			} else if err != nil || output.String() != want {
				t.Fatal("helper output, stdin, or environment changed")
			}
		})
	}
}

func TestMachineWindowsHelper_EnvironmentBlock(t *testing.T) {
	for _, env := range [][]string{{}, {"DUP=first", "dup=last", "VALUE=λ", "=C:=C:\\"}} {
		command := &exec.Cmd{Path: os.Args[0], Env: env}
		_, _, block, err := machineWindowsCommand(command)
		if err != nil || len(block) < 2 || block[len(block)-1] != 0 || block[len(block)-2] != 0 {
			t.Fatal("environment block was malformed")
		}
		decoded := strings.Split(string(utf16.Decode(block)), "\x00")
		var duplicates int
		for _, entry := range decoded {
			if strings.HasPrefix(strings.ToLower(entry), "dup=") {
				duplicates++
				if entry != "dup=last" {
					t.Fatal("duplicate environment did not preserve the final value")
				}
			}
		}
		if len(env) > 0 && duplicates != 1 {
			t.Fatal("case-insensitive environment keys were not deduplicated")
		}
	}
}

func TestMachineWindowsHelper_InvalidSetup(t *testing.T) {
	for _, invalid := range []string{"missing", "nul-argument", "nul-environment", "working-directory", "stdin", "stderr", "wait-delay", "process-attributes"} {
		t.Run(invalid, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			path := filepath.Join(t.TempDir(), "must-not-start")
			command := machineCredentialHelperCommand(ctx, "touch", path)
			command.Stdout = io.Discard
			switch invalid {
			case "missing":
				command.Path = path + "-SECRET-missing.exe"
			case "nul-argument":
				command.Args = append(command.Args, "\x00SECRET")
			case "nul-environment":
				command.Env = append(command.Env, "INVALID=\x00SECRET")
			case "working-directory":
				command.Dir = t.TempDir()
			case "stdin":
				command.Stdin = strings.NewReader("SECRET")
			case "stderr":
				command.Stderr = &bytes.Buffer{}
			case "wait-delay":
				command.WaitDelay = time.Second
			case "process-attributes":
				command.SysProcAttr = &syscall.SysProcAttr{}
			}
			err := runMachinePasswordCommand(ctx, command)
			if err == nil || strings.Contains(err.Error(), "SECRET") {
				t.Fatal("invalid setup started or disclosed native details")
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unsupported setup executed the helper")
			}
		})
	}
}

func TestMachineWindowsHelper_Descendants(t *testing.T) {
	for _, earlyExit := range []bool{false, true} {
		t.Run(fmt.Sprint(earlyExit), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			mode := "descendant"
			if earlyExit {
				mode = "early-descendant"
			}
			path := filepath.Join(t.TempDir(), "descendant.pid")
			gate := filepath.Join(t.TempDir(), "allow-main-exit")
			command := machineCredentialHelperCommand(ctx, mode, path, gate)
			command.Stdout = io.Discard
			done := machineRunHelperAsync(t, ctx, cancel, command)
			process := machineHelperReady(t, path)
			if earlyExit {
				start := time.Now()
				if err := os.WriteFile(gate, []byte("exit"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := machineWaitHelper(t, done, 2*time.Second); err != nil {
					t.Fatal("early shell exit did not stop inherited-output descendants")
				}
				if time.Since(start) > 2*time.Second {
					t.Fatal("early exit cleanup exceeded 2 seconds")
				}
				machineAssertProcessStopped(t, process)
				return
			}
			start := time.Now()
			cancel()
			cancel()
			if err := machineWaitHelper(t, done, 2*time.Second); !errors.Is(err, context.Canceled) || time.Since(start) > 2*time.Second {
				t.Fatal("descendant cancellation did not join promptly")
			}
			machineAssertProcessStopped(t, process)
		})
	}
}

func TestMachineWindowsHelper_IndependentInvocations(t *testing.T) {
	ctxA, cancelA := context.WithCancel(context.Background())
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelA()
	defer cancelB()
	pathA, pathB := filepath.Join(t.TempDir(), "A.pid"), filepath.Join(t.TempDir(), "B.pid")
	commandA, commandB := machineCredentialHelperCommand(ctxA, "hold", pathA), machineCredentialHelperCommand(ctxB, "hold", pathB)
	commandA.Stdout, commandB.Stdout = io.Discard, io.Discard
	doneA := machineRunHelperAsync(t, ctxA, cancelA, commandA)
	doneB := machineRunHelperAsync(t, ctxB, cancelB, commandB)
	processA, processB := machineHelperReady(t, pathA), machineHelperReady(t, pathB)
	var cancels sync.WaitGroup
	for range 4 {
		cancels.Go(cancelA)
	}
	cancels.Wait()
	if err := machineWaitHelper(t, doneA, 2*time.Second); !errors.Is(err, context.Canceled) {
		t.Fatal("first invocation did not cancel")
	}
	machineAssertProcessStopped(t, processA)
	if state, err := windows.WaitForSingleObject(processB, 0); err != nil || state != uint32(windows.WAIT_TIMEOUT) {
		t.Fatal("canceling A stopped independent invocation B")
	}
	cancelB()
	if err := machineWaitHelper(t, doneB, 2*time.Second); !errors.Is(err, context.Canceled) {
		t.Fatal("second invocation did not join")
	}
	machineAssertProcessStopped(t, processB)
}

func TestMachineWindowsHelper_CommandSemantics(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spaces λ")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "helper copy.exe")
	source, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	copy, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(copy, source)
	closeErr := copy.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatal("test-owned executable copy failed")
	}
	batch := filepath.Join(dir, "synthetic command.cmd")
	if err := os.WriteFile(batch, []byte("@echo off\r\necho batch-ok\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, command, want string }{
		{"expansion", "echo %PGSAVVY_MACHINE_HELPER_VALUE%", "expanded\r\n"},
		{"metacharacters", "echo before&echo after", "before\r\nafter\r\n"},
		{"batch", `call "` + batch + `"`, "batch-ok\r\n"},
		{"quoted-executable", `call "` + path + `" -test.run=^TestMachineCredentialHelperProcess$ -- output "native λ"`, "native λ"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			makeCommand := func() *exec.Cmd {
				c := exec.CommandContext(ctx, "cmd", "/C", tc.command)
				c.Env = append(scrubbedEnv(os.Environ()), machineHelperFixtureEnv+"=1", "PGSAVVY_MACHINE_HELPER_VALUE=expanded")
				c.Stderr = io.Discard
				return c
			}
			var baseline, supervised bytes.Buffer
			reference := makeCommand()
			reference.Stdout = &baseline
			if err := reference.Run(); err != nil || baseline.String() != tc.want {
				t.Fatal("native cmd /C baseline did not meet the compatibility fixture")
			}
			command := makeCommand()
			command.Stdout = &supervised
			if err := runMachinePasswordCommand(ctx, command); err != nil || supervised.String() != baseline.String() {
				t.Fatal("supervision changed cmd /C semantics")
			}
		})
	}
}
