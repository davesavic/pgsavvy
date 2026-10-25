//go:build linux || darwin || windows

package config

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/models"
)

type nativeConnectionsTestFs struct {
	afero.Fs
	beforeRename func() error
}

func (fs *nativeConnectionsTestFs) LockConnections(path string, deadline time.Time) (string, func(), error) {
	return nativeConnectionsWriteLock(path, deadline)
}

func (fs *nativeConnectionsTestFs) Rename(old, new string) error {
	if fs.beforeRename != nil {
		if err := fs.beforeRename(); err != nil {
			return err
		}
	}
	return fs.Fs.Rename(old, new)
}

func TestConnectionsWriteLock_RevocationInterleaving(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.yml")
	fs := afero.NewOsFs()
	initial := models.Connection{Name: "dev", CLIAccess: models.CLIAccessRowRead}
	if err := SaveConnections(fs, path, []models.Connection{initial}); err != nil {
		t.Fatal(err)
	}
	entered, resume := make(chan struct{}), make(chan struct{})
	staleFS := &nativeConnectionsTestFs{Fs: fs, beforeRename: func() error {
		close(entered)
		<-resume
		return nil
	}}
	initial.Label = "stale edit"
	staleResult := make(chan error, 1)
	go func() { staleResult <- UpdateConnection(staleFS, path, "dev", initial) }()
	<-entered // The old snapshot check has finished, but rename has not.
	revoked := models.Connection{Name: "dev", CLIAccess: models.CLIAccessDisabled}
	err := UpdateConnectionWithCLIAccess(fs, path, "dev", revoked, models.CLIAccessRowRead)
	close(resume)
	if staleErr := <-staleResult; staleErr != nil && !errors.Is(staleErr, ErrConnectionsChanged) {
		t.Fatal(staleErr)
	}
	if err != nil {
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatal(err)
		}
		// A busy revoker retries after the editor releases its transaction.
		if err := UpdateConnectionWithCLIAccess(fs, path, "dev", revoked, models.CLIAccessRowRead); err != nil {
			t.Fatal(err)
		}
	}
	got, err := LoadConnections(fs, path)
	if err != nil || len(got) != 1 || got[0].CLIAccess != models.CLIAccessDisabled {
		t.Fatalf("committed revocation overwritten: %v, %v", got, err)
	}
	// A subsequently saved stale form must also re-read the revoked policy.
	if err := UpdateConnection(fs, path, "dev", initial); err != nil {
		t.Fatal(err)
	}
	got, err = LoadConnections(fs, path)
	if err != nil || got[0].CLIAccess != models.CLIAccessDisabled || got[0].Label != initial.Label {
		t.Fatalf("stale form restored policy: %v, %v", got, err)
	}
}

type connectionsChild struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	output *bufio.Reader
	stderr bytes.Buffer
	waited bool
}

func startConnectionsChild(t *testing.T, path, action string) *connectionsChild {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	child := &connectionsChild{cmd: exec.CommandContext(ctx, os.Args[0], "-test.run=^TestConnectionsWriteLockProcessHelper$")}
	child.cmd.Env = append(os.Environ(), "PGSAVVY_LOCK_TEST_PATH="+path, "PGSAVVY_LOCK_TEST_ACTION="+action)
	child.cmd.Stderr = &child.stderr
	input, err := child.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	child.input = input
	output, err := child.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	child.output = bufio.NewReader(output)
	if err := child.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = child.input.Close()
		if !child.waited {
			_ = child.cmd.Process.Kill()
			_ = child.cmd.Wait()
		}
	})
	ready, err := child.output.ReadString('\n')
	if err != nil || ready != "ready\n" {
		t.Fatalf("child readiness %q: %v", ready, err)
	}
	return child
}

func (child *connectionsChild) finish(t *testing.T) {
	t.Helper()
	if err := child.input.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(child.output)
	if err != nil {
		t.Fatal(err)
	}
	err = child.cmd.Wait()
	child.waited = true
	if err != nil {
		t.Fatalf("child failed: %v\n%s\n%s", err, output, child.stderr.String())
	}
}

func TestConnectionsWriteLockProcessHelper(t *testing.T) {
	action := os.Getenv("PGSAVVY_LOCK_TEST_ACTION")
	if action == "" {
		return
	}
	path := os.Getenv("PGSAVVY_LOCK_TEST_PATH")
	fs := afero.NewOsFs()
	if action == "hold" {
		_, release, err := nativeConnectionsWriteLock(path, time.Now().Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		defer release()
	}
	fmt.Println("ready")
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
	// Fixture writers retry bounded contention; production calls retain their 250ms budget.
	deadline := time.Now().Add(5 * time.Second)
	retried := false
	for {
		var err error
		switch action {
		case "hold":
		case "append":
			err = AppendConnection(fs, path, models.Connection{Name: "new"})
		case "delete":
			err = DeleteConnection(fs, path, "delete")
		case "rename":
			err = UpdateConnection(fs, path, "rename", models.Connection{Name: "renamed"})
		case "revoke":
			err = UpdateConnectionWithCLIAccess(fs, path, "dev", models.Connection{Name: "dev", CLIAccess: models.CLIAccessDisabled}, models.CLIAccessRowRead)
		default:
			t.Fatalf("unknown helper action %q", action)
		}
		if err == nil {
			return
		}
		var lockErr *connectionsLockError
		if !errors.As(err, &lockErr) || !lockErr.Temporary() || !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("writer %s exhausted contention retries: %v", action, err)
		}
		if !retried {
			fmt.Println("retry")
			retried = true
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestConnectionsWriteLock_ProcessSerialization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "connections.yml")
	fs := afero.NewOsFs()
	if err := SaveConnections(fs, path, []models.Connection{{Name: "dev", CLIAccess: models.CLIAccessRowRead}, {Name: "rename"}, {Name: "delete"}}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lockInfo, err := os.Stat(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	holder := startConnectionsChild(t, path, "hold")
	for _, write := range []func() error{
		func() error { return SaveConnections(fs, path, nil) },
		func() error { return AppendConnection(fs, path, models.Connection{Name: "extra"}) },
		func() error { return DeleteConnection(fs, path, "delete") },
		func() error { return UpdateConnection(fs, path, "dev", models.Connection{Name: "dev"}) },
		func() error {
			return UpdateConnectionWithCLIAccess(fs, path, "dev", models.Connection{Name: "dev"}, models.CLIAccessRowRead)
		},
	} {
		start := time.Now()
		err := write()
		if !errors.Is(err, os.ErrDeadlineExceeded) || time.Since(start) > 750*time.Millisecond {
			t.Fatalf("unbounded or successful contended write: %v (%s)", err, time.Since(start))
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("contention changed profile bytes")
		}
	}
	var children []*connectionsChild
	for _, action := range []string{"append", "delete", "rename", "revoke"} {
		children = append(children, startConnectionsChild(t, path, action))
	}
	// All four processes are ready before any mutation starts.
	for _, child := range children {
		if _, err := child.input.Write([]byte("start")); err != nil {
			t.Fatal(err)
		}
		// io.Copy in the helper waits for EOF, so release all before joining.
		if err := child.input.Close(); err != nil {
			t.Fatal(err)
		}
	}
	// Every writer must encounter the held lock before any can commit.
	for _, child := range children {
		retry, err := child.output.ReadString('\n')
		if err != nil || retry != "retry\n" {
			t.Fatalf("writer did not retry contention: %q, %v", retry, err)
		}
	}
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(before, after) {
		t.Fatal("retrying writers changed profile bytes while lock held")
	}
	holder.finish(t)
	for _, child := range children {
		output, err := io.ReadAll(child.output)
		if err != nil {
			t.Fatal(err)
		}
		err = child.cmd.Wait()
		child.waited = true
		if err != nil {
			t.Fatalf("parallel writer: %v\n%s\n%s", err, output, child.stderr.String())
		}
	}
	got, err := LoadConnections(fs, path)
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]models.Connection)
	for _, conn := range got {
		byName[conn.Name] = conn
	}
	if len(got) != 3 || byName["dev"].CLIAccess != models.CLIAccessDisabled || byName["renamed"].Name != "renamed" || byName["new"].Name != "new" {
		t.Fatalf("lost concurrent mutation: %v", got)
	}
	afterLock, err := os.Stat(path + ".lock")
	if err != nil || !os.SameFile(lockInfo, afterLock) {
		t.Fatalf("lock inode replaced: %v", err)
	}
}

func TestConnectionsWriteLock_FailureAndCrashRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.yml")
	fs := afero.NewOsFs()
	failing := &nativeConnectionsTestFs{Fs: fs, beforeRename: func() error { return errors.New("simulated rename failure") }}
	if err := SaveConnections(failing, path, nil); err == nil || !strings.Contains(err.Error(), "simulated rename failure") {
		t.Fatalf("write error not preserved: %v", err)
	}
	if err := AppendConnection(fs, path, models.Connection{Name: "dev"}); err != nil {
		t.Fatalf("failure retained lock: %v", err)
	}
	child := startConnectionsChild(t, path, "hold")
	if err := child.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := child.cmd.Wait(); err == nil {
		t.Fatal("killed helper exited successfully")
	}
	child.waited = true
	if err := AppendConnection(fs, path, models.Connection{Name: "after-crash"}); err != nil {
		t.Fatalf("process exit retained lock: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 2 {
		t.Fatalf("failure/crash leaked temporary or removed lock: %v, %v", entries, err)
	}
}

func TestConnectionsWriteLock_AliasesAndUnsafeTargets(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "private", "connections.yml")
	fs := afero.NewOsFs()
	if err := SaveConnections(fs, path, nil); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		for _, name := range []string{path, path + ".lock"} {
			info, err := os.Stat(name)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("private file mode: %v, %v", info, err)
			}
		}
		info, err := os.Stat(filepath.Dir(path))
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("private directory mode: %v, %v", info, err)
		}
	}
	alias := filepath.Join(root, "alias")
	symlinkErr := os.Symlink(filepath.Dir(path), alias)
	if symlinkErr != nil {
		t.Logf("symlink fixture unavailable: %v", symlinkErr)
	}
	canonical, release, err := nativeConnectionsWriteLock(path, time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	aliases := []string{filepath.Join(root, "private", "..", "private", "connections.yml")}
	if symlinkErr == nil {
		aliases = append(aliases, filepath.Join(alias, "connections.yml"))
	}
	if cwd, err := os.Getwd(); err == nil {
		if relative, err := filepath.Rel(cwd, path); err == nil {
			aliases = append(aliases, relative)
		}
	}
	for _, alias := range aliases {
		if err := SaveConnections(fs, alias, nil); !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Errorf("alias escaped lock %q (canonical %q): %v", alias, canonical, err)
		}
	}
	release()
	outside := filepath.Join(root, "outside")
	before := []byte("must remain untouched")
	if err := os.WriteFile(outside, before, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", ".lock"} {
		unsafe := filepath.Join(root, "unsafe"+suffix)
		t.Run("symlink"+suffix, func(t *testing.T) {
			if symlinkErr != nil {
				t.Skipf("symlink fixture unavailable: %v", symlinkErr)
			}
			if err := os.Symlink(outside, unsafe); err != nil {
				t.Fatal(err)
			}
			if err := SaveConnections(fs, filepath.Join(root, "unsafe"), nil); err == nil {
				t.Fatal("final symlink accepted")
			}
			if err := os.Remove(unsafe); err != nil {
				t.Fatal(err)
			}
		})
		if err := os.Link(outside, unsafe); err != nil {
			t.Fatal(err)
		}
		if err := SaveConnections(fs, filepath.Join(root, "unsafe"), nil); err == nil {
			t.Fatal("hard-linked target accepted")
		}
		if err := os.Remove(unsafe); err != nil {
			t.Fatal(err)
		}
	}
	after, err := os.ReadFile(outside)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("unsafe target was modified")
	}
	if err := os.Mkdir(filepath.Join(root, "directory.yml"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := SaveConnections(fs, filepath.Join(root, "directory.yml"), nil); err == nil {
		t.Fatal("directory accepted as profiles")
	}
	// Intentional replacement must not require decoding the existing file.
	if err := os.WriteFile(path, []byte("invalid: [yaml"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveConnections(fs, path, nil); err != nil {
		t.Fatalf("full replacement became a merging edit: %v", err)
	}
}
