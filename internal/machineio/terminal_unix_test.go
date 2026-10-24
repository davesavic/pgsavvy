//go:build linux || darwin

package machineio

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func approvalPrivateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestMachineTerminalApprovalLock(t *testing.T) {
	dir := approvalPrivateDir(t)
	lock, err := openApprovalLock(dir, "approval.lock")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(filepath.Join(dir, "approval.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if second, err := openApprovalLock(dir, "approval.lock"); err == nil {
		_ = unix.Close(second)
		t.Fatal("busy lock accepted")
	}
	if err := unix.Close(lock); err != nil {
		t.Fatal(err)
	}
	lock, err = openApprovalLock(dir, "approval.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := unix.Close(lock); err != nil {
			t.Error(err)
		}
	}()
	after, err := os.Stat(filepath.Join(dir, "approval.lock"))
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("lock inode changed")
	}
}

func TestMachineTerminalApprovalUnsafeLock(t *testing.T) {
	for _, kind := range []string{"mode", "symlink", "hardlink", "directory", "fifo", "owner"} {
		t.Run(kind, func(t *testing.T) {
			dir := approvalPrivateDir(t)
			path := filepath.Join(dir, "approval.lock")
			switch kind {
			case "symlink":
				if err := os.Symlink("target", path); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := unix.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.WriteFile(path, []byte("retained"), 0o600); err != nil {
					t.Fatal(err)
				}
				if kind == "mode" {
					if err := os.Chmod(path, 0o640); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "hardlink" {
					if err := os.Link(path, path+".alias"); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "owner" {
					if os.Geteuid() != 0 {
						var info unix.Stat_t
						if err := unix.Stat(path, &info); err != nil {
							t.Fatal(err)
						}
						info.Uid++
						if safeApprovalLock(info) {
							t.Fatal("foreign owner accepted")
						}
						return
					}
					if err := os.Chown(path, 1, -1); err != nil {
						t.Fatal(err)
					}
				}
			}
			if fd, err := openApprovalLock(dir, "approval.lock"); err == nil {
				_ = unix.Close(fd)
				t.Fatal("unsafe lock accepted")
			}
		})
	}
}

func TestMachineTerminalApprovalCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if terminal, err := OpenApprovalTerminal(ctx); err != context.Canceled || terminal != nil {
		t.Fatal("canceled acquisition accepted")
	}
}

func TestMachineTerminalApprovalUnsafeNamespace(t *testing.T) {
	for _, kind := range []string{"mode", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			directory := approvalPrivateDir(t)
			namespace := directory
			if kind == "mode" {
				if err := os.Chmod(directory, 0o777); err != nil {
					t.Fatal(err)
				}
			} else {
				namespace = filepath.Join(approvalPrivateDir(t), "namespace-link")
				if err := os.Symlink(directory, namespace); err != nil {
					t.Fatal(err)
				}
			}
			terminal, err := openApprovalTerminal(context.Background(), namespace, "approval.lock")
			if terminal != nil {
				_ = terminal.Close()
				t.Fatal("unsafe namespace acquired terminal")
			}
			if err != errUnavailable {
				t.Fatalf("got %v want unavailable", err)
			}
			// Lock acquisition precedes /dev/tty acquisition and every input read.
			if _, err := os.Lstat(filepath.Join(directory, "approval.lock")); !os.IsNotExist(err) {
				t.Fatalf("unsafe namespace reached lock creation before terminal acquisition: %v", err)
			}
		})
	}
}

func TestMachineTerminalApprovalProcess(t *testing.T) {
	if mode := os.Getenv("PGSAVVY_APPROVAL_CHILD"); mode != "" {
		// A disposable PTY hangup must reach Read rather than kill the fixture.
		signal.Ignore(syscall.SIGHUP)
		dir := os.Getenv("PGSAVVY_APPROVAL_LOCKDIR")
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		terminal, err := openApprovalTerminal(ctx, dir, "ceremony.lock")
		if mode == "competitor" {
			if err == nil {
				_ = terminal.Close()
				t.Fatal("competing ceremony acquired terminal")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = terminal.Close() }()
		fmt.Println("READY")
		if mode == "success" {
			gate := os.NewFile(3, "parent-gate")
			defer func() {
				if err := gate.Close(); err != nil {
					t.Error(err)
				}
			}()
			if _, err := io.ReadFull(gate, make([]byte, 1)); err != nil {
				t.Fatal(err)
			}
			competitor := exec.Command(os.Args[0], "-test.run=^TestMachineTerminalApprovalProcess$")
			competitor.Env = append(os.Environ(), "PGSAVVY_APPROVAL_CHILD=competitor", "XDG_STATE_HOME="+dir+"/other-state", "TMPDIR="+dir+"/other-tmp")
			competitor.Stdin = os.Stdin
			if out, err := competitor.CombinedOutput(); err != nil {
				t.Fatalf("competitor: %v %s", err, out)
			}
			var got [9]byte
			if _, err := io.ReadFull(terminal, got[:]); err != nil || string(got[:]) != "selected\n" {
				t.Fatalf("response stolen: %q %v", got, err)
			}
			if _, err := terminal.Write([]byte("OWNED\n")); err != nil {
				t.Fatal(err)
			}
			return
		}
		done := make(chan error, 1)
		go func() { var one [1]byte; _, err := terminal.Read(one[:]); done <- err }()
		if mode == "deadline" {
			var stop context.CancelFunc
			ctx, stop = context.WithTimeout(ctx, 30*time.Millisecond)
			defer stop()
		}
		if mode == "closed" {
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("closed terminal read succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("closed terminal did not unblock")
			}
		} else {
			if mode != "close" {
				<-ctx.Done()
			}
			if err := terminal.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("closed read succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("owned read did not join")
			}
		}
		return
	}
	for _, mode := range []string{"success", "signal", "deadline", "close", "closed", "no-tty"} {
		t.Run(mode, func(t *testing.T) {
			master, slave := testPTY(t)
			before, termios := testFlags(t, slave), testTermios(t, slave)
			dir := approvalPrivateDir(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMachineTerminalApprovalProcess$")
			childMode := mode
			if mode == "no-tty" {
				childMode = "competitor"
			}
			cmd.Env = append(os.Environ(), "PGSAVVY_APPROVAL_CHILD="+childMode, "PGSAVVY_APPROVAL_LOCKDIR="+dir, "XDG_STATE_HOME="+dir+"/first-state", "TMPDIR="+dir+"/first-tmp")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			if mode != "no-tty" {
				cmd.Stdin = slave
				cmd.SysProcAttr.Setctty = true
				cmd.SysProcAttr.Ctty = 0
			}
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := read.Close(); err != nil {
					t.Error(err)
				}
			}()
			defer func() {
				if err := write.Close(); err != nil {
					t.Error(err)
				}
			}()
			cmd.ExtraFiles = []*os.File{read}
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var diagnostic bytes.Buffer
			cmd.Stderr = &diagnostic
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()
			reader := bufio.NewReader(out)
			if mode != "no-tty" {
				line, err := reader.ReadString('\n')
				if err != nil || line != "READY\n" {
					t.Fatalf("startup: %q %v %s", line, err, diagnostic.String())
				}
			}
			switch mode {
			case "success":
				if _, err := master.WriteString("selected\n"); err != nil {
					t.Fatal(err)
				}
				if _, err := write.WriteString("!"); err != nil {
					t.Fatal(err)
				}
			case "signal":
				if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
					t.Fatal(err)
				}
			case "closed":
				_ = master.Close()
			}
			rest, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("child %s: %v %s %s", mode, err, rest, diagnostic.String())
			}
			if mode != "closed" && (testFlags(t, slave) != before || !reflect.DeepEqual(termios, testTermios(t, slave))) {
				t.Fatal("parent flags or termios changed")
			}
			if mode == "success" && strings.Contains(string(rest), "selected") {
				t.Fatal("terminal data escaped to stdout")
			}
		})
	}
}
