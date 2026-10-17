package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func helperArgs(mode string, args ...string) []string {
	return append([]string{"-test.run=^TestPGFixtureHelper$", "--", mode}, args...)
}

func TestRun(t *testing.T) {
	t.Setenv("GO_WANT_PGFIXTURE_HELPER", "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	log := filepath.Join(root, "private log")
	for _, code := range []int{0, 7} {
		args := append([]string{executable, log}, helperArgs("exit", strconv.Itoa(code))...)
		if got := run(args); got != code {
			t.Fatalf("exit = %d, want %d", got, code)
		}
		contents, err := os.ReadFile(log)
		if err != nil || string(contents) != "private stdout\n" {
			t.Fatalf("private stdout capture failed: %v", err)
		}
		contents, err = os.ReadFile(log + ".err")
		if err != nil || string(contents) != "private stderr\n" {
			t.Fatalf("private stderr capture failed: %v", err)
		}
	}
	if run(nil) != helperFailure || run([]string{executable, root}) != helperFailure || run([]string{filepath.Join(root, "missing"), log}) != helperFailure {
		t.Fatal("invalid input, file failure or launch failure accepted")
	}
	if got := run(append([]string{executable, log}, helperArgs("stdin")...)); got != 0 {
		t.Fatalf("stdin was not EOF: %d", got)
	}
}

func TestCloseFailureOverridesExpectedChildFailure(t *testing.T) {
	t.Setenv("GO_WANT_PGFIXTURE_HELPER", "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, helperArgs("exit", "2")...)
	command.Stdout, command.Stderr = io.Discard, io.Discard
	childErr := command.Run()
	if commandStatus(childErr, nil, nil) != 2 {
		t.Fatal("expected child exit not preserved")
	}
	closeErr := errors.New("synthetic close failure")
	for _, errs := range [][2]error{{closeErr, nil}, {nil, closeErr}, {closeErr, closeErr}} {
		if commandStatus(childErr, errs[0], errs[1]) != helperFailure {
			t.Fatal("child exit hid failed logging")
		}
	}
}

func TestControllerDoesNotPassCapturePipesToPersistentChild(t *testing.T) {
	t.Setenv("GO_WANT_PGFIXTURE_HELPER", "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	ready, stop, done := filepath.Join(root, "ready"), filepath.Join(root, "stop"), filepath.Join(root, "done")
	t.Cleanup(func() {
		_ = os.WriteFile(stop, nil, 0o600)
		if _, err := os.Stat(ready); err == nil {
			if !waitFile(done, 5*time.Second) {
				t.Error("owned synthetic server did not stop")
			}
		}
	})
	args := []string{executable, filepath.Join(root, "controller log")}
	args = append(args, helperArgs("daemon-parent", ready, stop, done)...)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, helperArgs("controller", args...)...)
	command.WaitDelay = time.Second
	output, err := command.CombinedOutput()
	if err != nil || len(output) != 0 {
		t.Fatalf("controller capture did not join privately: %v", err)
	}
	if _, err := os.Stat(ready); err != nil {
		t.Fatalf("server did not start: %v", err)
	}
	if _, err := os.Stat(done); !os.IsNotExist(err) {
		t.Fatal("controller stopped the intentional server descendant")
	}
}

func waitFile(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func TestPGFixtureHelper(t *testing.T) {
	if os.Getenv("GO_WANT_PGFIXTURE_HELPER") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	args = args[1:]
	switch args[0] {
	case "controller":
		os.Exit(run(args[1:]))
	case "exit":
		_, _ = fmt.Fprintln(os.Stdout, "private stdout")
		_, _ = fmt.Fprintln(os.Stderr, "private stderr")
		code, _ := strconv.Atoi(args[1])
		os.Exit(code)
	case "stdin":
		contents, err := io.ReadAll(os.Stdin)
		if err != nil || len(contents) != 0 {
			os.Exit(3)
		}
	case "daemon-parent":
		executable, _ := os.Executable()
		child := exec.Command(executable, helperArgs("daemon", args[1:]...)...)
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if child.Start() != nil || !waitFile(args[1], 5*time.Second) {
			os.Exit(4)
		}
		_ = child.Process.Release()
	case "daemon":
		if os.WriteFile(args[1], nil, 0o600) != nil {
			os.Exit(5)
		}
		_ = waitFile(args[2], 10*time.Second)
		_ = os.Stdout.Close()
		_ = os.Stderr.Close()
		if os.WriteFile(args[3], nil, 0o600) != nil {
			os.Exit(6)
		}
	default:
		os.Exit(2)
	}
	os.Exit(0)
}
