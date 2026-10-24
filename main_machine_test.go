//go:build linux || darwin

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/internal/machineio"
	"golang.org/x/sys/unix"
)

var machineTestBinary string

func TestMain(m *testing.M) {
	if handled, exit := machineio.RunHelper(); handled {
		os.Exit(exit)
	}
	if binary := os.Getenv("PGSAVVY_TEST_BINARY"); binary != "" {
		if !filepath.IsAbs(binary) {
			os.Exit(1)
		}
		machineTestBinary = binary
		os.Exit(m.Run())
	}
	dir, err := os.MkdirTemp("", "pgsavvy-machine-test-")
	if err != nil {
		os.Exit(1)
	}
	machineTestBinary = filepath.Join(dir, "pgsavvy")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	command := exec.CommandContext(ctx, "go", "build", "-o", machineTestBinary, ".")
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	output, err := command.CombinedOutput()
	cancel()
	if err != nil {
		_, _ = os.Stderr.Write(output)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	exit := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(exit)
}

func machineTestEnv(root string) []string {
	environment := []string{}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "PG") || strings.HasPrefix(name, "XDG_") || strings.HasPrefix(name, "_PGSAVVY_") || name == "HOME" || name == "SSH_AUTH_SOCK" || name == "ENV" || name == "BASH_ENV" || name == "SHELL" {
			continue
		}
		environment = append(environment, entry)
	}
	for _, name := range []string{"CONFIG", "DATA", "STATE", "CACHE"} {
		environment = append(environment, "XDG_"+name+"_HOME="+filepath.Join(root, strings.ToLower(name)))
	}
	return append(environment, "XDG_RUNTIME_DIR="+filepath.Join(root, "runtime"), "HOME="+filepath.Join(root, "home"), "PGPASSFILE="+filepath.Join(root, "no-pass"), "PGSERVICEFILE="+filepath.Join(root, "no-service"), "SSH_AUTH_SOCK="+filepath.Join(root, "no-agent"), "SHELL=/bin/sh", "ENV=", "BASH_ENV=")
}

func machineTestRun(t *testing.T, root, input string, want int, args ...string) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, machineTestBinary, args...)
	command.Env = machineTestEnv(root)
	command.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	exit := 0
	if err != nil {
		var status *exec.ExitError
		if !errors.As(err, &status) {
			t.Fatalf("CLI process failed: %v", err)
		}
		exit = status.ExitCode()
	}
	if exit != want {
		t.Fatalf("%v: exit %d, want %d, stdout=%q stderr=%q", args, exit, want, stdout.String(), stderr.String())
	}
	doc := machineTestDocument(t, stdout.Bytes(), want)
	if stderr.Len() > 0 {
		var diagnostic map[string]any
		if err := json.Unmarshal(stderr.Bytes(), &diagnostic); err != nil || len(diagnostic) != 2 || diagnostic["command"] != doc["command"] || diagnostic["exit"] != float64(want) {
			t.Fatalf("unsafe CLI diagnostic: %q", stderr.String())
		}
	}
	return doc
}

func machineTestDocument(t *testing.T, output []byte, exit int) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(output))
	var doc map[string]any
	if len(output) == 0 || output[len(output)-1] != '\n' || decoder.Decode(&doc) != nil || decoder.Decode(new(any)) != io.EOF {
		t.Fatalf("CLI did not return one newline-terminated JSON document: %q", output)
	}
	if doc["schema_version"] != float64(1) || doc["ok"] != (exit == 0) {
		t.Fatalf("CLI envelope mismatch: %v", doc)
	}
	return doc
}

func TestMachineCLINative_Discovery(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{{"--help"}, {"connections", "--help"}, {"schema", "--help"}, {"query", "--help"}, {"llm-help"}, {"llm-help", "--help"}} {
		machineTestRun(t, root, "", 0, args...)
	}
	doc := machineTestRun(t, root, "", 0, "capabilities")
	data := doc["data"].(map[string]any)
	if data["platform"] != runtime.GOOS || len(data["commands"].([]any)) != 10 {
		t.Fatal("capabilities did not match native command registry")
	}
	doc = machineTestRun(t, root, "", 0, "connections", "list")
	if profiles, ok := doc["data"].([]any); !ok || len(profiles) != 0 {
		t.Fatal("missing profiles did not produce an empty array")
	}
	machineTestRun(t, root, "", 2, "not-a-command")
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatal("offline CLI created configuration, logs, history or other state")
	}
	t.Run("llm-help", machineTestLLMHelpOffline)
}

func TestMachineCLINative_ProfilePolicyAndInput(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "config", "pgsavvy")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "connections.yml")
	profile := "connections:\n  - name: reporting\n    driver: postgres\n    cli_access: row-read\n    password_command: 'exit 99'\n  - name: disabled\n    driver: postgres\n    password_command: 'exit 99'\n"
	if err := os.WriteFile(path, []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	doc := machineTestRun(t, root, "", 0, "connections", "show", "reporting")
	if doc["data"].(map[string]any)["cli_access"] != "row-read" {
		t.Fatal("enabled CLI policy not reported")
	}
	doc = machineTestRun(t, root, "", 4, "connections", "test", "disabled")
	if doc["error"].(map[string]any)["code"] != "POLICY_DENIED" {
		t.Fatal("disabled profile did not fail closed")
	}
	doc = machineTestRun(t, root, "DELETE FROM forbidden", 2, "query", "--connection", "reporting", "--file", "-")
	if doc["error"].(map[string]any)["code"] != "UNSUPPORTED_SQL" {
		t.Fatal("piped query input did not reach safe SQL validation")
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != profile {
		t.Fatal("CLI rewrote profile permissions or content")
	}
}

func TestMachineCLINative_ShellPipe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/bin/sh", "-c", `"$1" --help | cat`, "pgsavvy-pipe", machineTestBinary)
	command.Env = machineTestEnv(t.TempDir())
	output, err := command.Output()
	if err != nil {
		t.Fatal("shell pipeline failed")
	}
	if machineTestDocument(t, output, 0)["command"] != "help" {
		t.Fatal("shell pipe bypassed machine help")
	}
}

func TestMachineCLINative_HelperExecutable(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = read.Close(); _ = write.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, machineTestBinary)
	command.Env = []string{"_PGSAVVY_MACHINE_IO=write"}
	command.ExtraFiles = []*os.File{write}
	requests, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	responses, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = requests.Close()
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	ready := make([]byte, len("pgsavvy-io-1\n"))
	if _, err := io.ReadFull(responses, ready); err != nil || string(ready) != "pgsavvy-io-1\n" {
		t.Fatal("shipping executable did not enter its private worker")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], 2)
	if _, err := requests.Write(append(header[:], 'o', 'k')); err != nil {
		t.Fatal(err)
	}
	var ack [8]byte
	if _, err := io.ReadFull(responses, ack[:]); err != nil || binary.BigEndian.Uint32(ack[:4]) != 2 || binary.BigEndian.Uint32(ack[4:]) != 0 {
		t.Fatal("shipping worker did not acknowledge destination write")
	}
	var payload [2]byte
	if _, err := io.ReadFull(read, payload[:]); err != nil || string(payload[:]) != "ok" {
		t.Fatal("shipping worker changed destination bytes")
	}
	_ = requests.Close()
	if err := command.Wait(); err != nil || stderr.Len() != 0 {
		t.Fatal("private worker entered legacy startup or emitted diagnostics")
	}
}

func TestMachineCLINative_GroupSignal(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = read.Close(); _ = write.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, machineTestBinary, "query", "--connection", "missing", "--file", "-")
	command.Env = machineTestEnv(t.TempDir())
	command.Stdin = read
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = write.Close()
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	if _, err := write.Write([]byte(" ")); err != nil {
		t.Fatal(err)
	}
	raw, err := read.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	ready := false
	deadline := time.Now().Add(2 * time.Second)
	for !ready && time.Now().Before(deadline) {
		if err := raw.Control(func(fd uintptr) {
			poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			n, err := unix.Poll(poll, 0)
			ready = err == nil && n == 0
		}); err != nil {
			t.Fatal(err)
		}
		if !ready {
			time.Sleep(time.Millisecond)
		}
	}
	if !ready {
		t.Fatal("CLI never consumed input after installing signal handlers")
	}
	start := time.Now()
	if err := unix.Kill(-command.Process.Pid, unix.SIGINT); err != nil {
		t.Fatal(err)
	}
	err = command.Wait()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 6 || time.Since(start) > 3*time.Second {
		t.Fatalf("foreground cancellation failed: %v", err)
	}
	doc := machineTestDocument(t, stdout.Bytes(), 6)
	if doc["error"].(map[string]any)["code"] != "TIMEOUT" {
		t.Fatal("foreground cancellation lost usable publication worker")
	}
}

func TestMachineCLINative_InvalidInputHandle(t *testing.T) {
	input, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, machineTestBinary, "--help")
	command.Env = machineTestEnv(t.TempDir())
	command.Stdin = input
	output, err := command.Output()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("invalid startup stream did not fail safely: %v", err)
	}
	doc := machineTestDocument(t, output, 7)
	if doc["error"].(map[string]any)["code"] != "RESOURCE_FAILURE" {
		t.Fatal("startup failure lost safe resource envelope")
	}
}
