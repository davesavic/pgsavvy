//go:build integration && linux

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type processFlagStream struct {
	source, peer *os.File
	flags        int
	socket       bool
}

func processFlagControl(t *testing.T, file *os.File, fn func(int)) {
	t.Helper()
	raw, err := file.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Control(func(fd uintptr) { fn(int(fd)) }); err != nil {
		t.Fatal(err)
	}
}

func processFlagValue(t *testing.T, file *os.File) int {
	t.Helper()
	var flags int
	processFlagControl(t, file, func(fd int) {
		var err error
		flags, err = unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
		if err != nil {
			t.Fatal(err)
		}
	})
	return flags
}

func newProcessFlagStream(t *testing.T, socket, input, nonblocking bool) *processFlagStream {
	t.Helper()
	var fds [2]int
	var err error
	if socket {
		fds, err = unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	} else {
		err = unix.Pipe(fds[:])
	}
	if err != nil {
		t.Fatal(err)
	}
	if !socket && !input {
		fds[0], fds[1] = fds[1], fds[0]
	}
	for i, fd := range fds {
		unix.CloseOnExec(fd)
		if err := unix.SetNonblock(fd, i == 1 || nonblocking); err != nil {
			t.Fatal(err)
		}
	}
	s := &processFlagStream{source: os.NewFile(uintptr(fds[0]), "retained-source"), peer: os.NewFile(uintptr(fds[1]), "fixture-peer"), socket: socket}
	s.flags = processFlagValue(t, s.source)
	t.Cleanup(func() { _ = s.source.Close(); _ = s.peer.Close() })
	return s
}

func (s *processFlagStream) check(t *testing.T) {
	t.Helper()
	if got := processFlagValue(t, s.source); got != s.flags {
		t.Fatalf("inherited flags changed %#x -> %#x", s.flags, got)
	}
}

func processFlagDrain(t *testing.T, file *os.File) []byte {
	t.Helper()
	var data []byte
	processFlagControl(t, file, func(fd int) {
		var b [8192]byte
		for {
			n, err := unix.Read(fd, b[:])
			if n > 0 {
				data = append(data, b[:n]...)
			}
			if err == unix.EINTR {
				continue
			}
			if err == unix.EAGAIN || err == unix.EWOULDBLOCK || err == nil && n == 0 {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		}
	})
	return data
}

func (s *processFlagStream) fill(t *testing.T) {
	t.Helper()
	processFlagControl(t, s.source, func(fd int) {
		if err := unix.SetNonblock(fd, true); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFL, s.flags); err != nil {
				t.Error(err)
			}
		}()
		b := bytes.Repeat([]byte{'x'}, 4096)
		for {
			_, err := unix.Write(fd, b)
			if err == unix.EINTR {
				continue
			}
			if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		}
	})
	s.check(t)
}

func processFlagStart(t *testing.T, command *exec.Cmd) {
	t.Helper()
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
}

func processFlagWait(t *testing.T, command *exec.Cmd, want int, limit time.Duration) {
	t.Helper()
	start := time.Now()
	if got := processExit(command.Wait()); got != want || time.Since(start) > limit {
		t.Fatalf("exit %d want %d within %s (elapsed %s)", got, want, limit, time.Since(start))
	}
}

func processFlagConsumed(t *testing.T, command *exec.Cmd, input *processFlagStream) {
	t.Helper()
	// Only input consumption can remove these queued bytes. Merely duplicating
	// aliased stdout cannot establish this barrier, nor can restoring flags at exit.
	if _, err := input.peer.Write([]byte("SELECT ")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		available := -1
		processFlagControl(t, input.source, func(fd int) {
			var err error
			available, err = unix.IoctlGetInt(fd, unix.TIOCINQ)
			if err != nil {
				t.Fatal(err)
			}
		})
		if available == 0 && processSignalReady(command.Process.Pid, 10*time.Millisecond) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("child did not consume input before live flag check")
}

func processFlagReusable(t *testing.T, stream *processFlagStream, input bool) {
	t.Helper()
	if input {
		if _, err := stream.peer.Write([]byte("z")); err != nil {
			t.Fatal("retained input peer unusable")
		}
		var got [1]byte
		processFlagControl(t, stream.source, func(fd int) {
			if n, err := unix.Read(fd, got[:]); n != 1 || err != nil || got[0] != 'z' {
				t.Fatal("retained input unusable")
			}
		})
	} else {
		processFlagControl(t, stream.source, func(fd int) {
			if n, err := unix.Write(fd, []byte("z")); n != 1 || err != nil {
				t.Fatal("retained output unusable")
			}
		})
		if got := processFlagDrain(t, stream.peer); string(got) != "z" {
			t.Fatalf("retained output bytes %q", got)
		}
	}
	stream.check(t)
}

func TestMachineCLIProcess_StreamFlags(t *testing.T) {
	for _, socket := range []bool{false, true} {
		for _, nonblocking := range []bool{false, true} {
			name := fmt.Sprintf("socket=%t/nonblocking=%t", socket, nonblocking)
			t.Run(name, func(t *testing.T) {
				for _, route := range []struct {
					name string
					args []string
					exit int
				}{
					{"capabilities", []string{"capabilities"}, 0}, {"help", []string{"--help"}, 0}, {"rejected", []string{"not-a-command"}, 2},
				} {
					t.Run(route.name, func(t *testing.T) {
						f := firstProcess(t, false)
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						in, out, diagnostic := newProcessFlagStream(t, socket, true, nonblocking), newProcessFlagStream(t, socket, false, nonblocking), newProcessFlagStream(t, socket, false, nonblocking)
						command := f.command(ctx, route.args...)
						command.Stdin, command.Stdout, command.Stderr = in.source, out.source, diagnostic.source
						processFlagStart(t, command)
						processFlagWait(t, command, route.exit, 5*time.Second)
						for _, s := range []*processFlagStream{in, out, diagnostic} {
							s.check(t)
						}
						processDocument(t, processFlagDrain(t, out.peer), processFlagDrain(t, diagnostic.peer), route.exit)
						processFlagReusable(t, in, true)
						processFlagReusable(t, out, false)
						processFlagReusable(t, diagnostic, false)
					})
				}
				for _, action := range []string{"timeout", "interrupt", "terminate"} {
					t.Run(action, func(t *testing.T) { processFlagLive(t, socket, nonblocking, action, "separate") })
				}
				for _, alias := range []string{"shared-output", "bidirectional"} {
					if alias == "bidirectional" && !socket {
						continue
					}
					t.Run(alias, func(t *testing.T) { processFlagLive(t, socket, nonblocking, "interrupt", alias) })
				}
				for _, canceled := range []bool{false, true} {
					t.Run(fmt.Sprintf("backpressure/canceled=%t", canceled), func(t *testing.T) { processFlagBackpressure(t, socket, nonblocking, canceled) })
				}
				for _, broken := range []string{"stdout", "stderr"} {
					t.Run("broken-"+broken, func(t *testing.T) {
						f := firstProcess(t, false)
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						in, out, diagnostic := newProcessFlagStream(t, socket, true, nonblocking), newProcessFlagStream(t, socket, false, nonblocking), newProcessFlagStream(t, socket, false, nonblocking)
						command := f.command(ctx, "capabilities")
						command.Stdin, command.Stdout, command.Stderr = in.source, out.source, diagnostic.source
						want := 0
						if broken == "stdout" {
							_ = out.peer.Close()
							want = 7
						} else {
							_ = diagnostic.peer.Close()
						}
						processFlagStart(t, command)
						processFlagWait(t, command, want, 5*time.Second)
						for _, s := range []*processFlagStream{in, out, diagnostic} {
							s.check(t)
						}
						if broken == "stderr" {
							processDocument(t, processFlagDrain(t, out.peer), nil, 0)
						}
					})
				}
				t.Run("unread-stderr", func(t *testing.T) {
					f := firstProcess(t, false)
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					in, out, diagnostic := newProcessFlagStream(t, socket, true, nonblocking), newProcessFlagStream(t, socket, false, nonblocking), newProcessFlagStream(t, socket, false, nonblocking)
					diagnostic.fill(t)
					command := f.command(ctx, "capabilities")
					command.Stdin, command.Stdout, command.Stderr = in.source, out.source, diagnostic.source
					processFlagStart(t, command)
					processFlagWait(t, command, 0, 2*time.Second)
					for _, s := range []*processFlagStream{in, out, diagnostic} {
						s.check(t)
					}
					processDocument(t, processFlagDrain(t, out.peer), nil, 0)
					processFlagDrain(t, diagnostic.peer)
					processFlagReusable(t, diagnostic, false)
				})
			})
		}
	}
	t.Run("default-timeout", func(t *testing.T) { processFlagLive(t, false, false, "default-timeout", "separate") })
	t.Run("alias-barrier", testProcessFlagAliasBarrier)
	t.Run("restore-at-exit", testProcessFlagRestoreAtExit)
	t.Run("acquisition-failure", testProcessFlagAcquisitionFailure)
}

func testProcessFlagRestoreAtExit(t *testing.T) {
	stream := newProcessFlagStream(t, false, true, false)
	// A description shared with a live child can be mutated and later restored.
	// The live observation must detect that mutation despite equal final flags.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/bin/sh", "-c", "exec sleep 10")
	command.Stdin = stream.source
	processFlagStart(t, command)
	processFlagControl(t, stream.source, func(fd int) {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFL, stream.flags|unix.O_NONBLOCK); err != nil {
			t.Fatal(err)
		}
	})
	live := processFlagValue(t, stream.source)
	processFlagControl(t, stream.source, func(fd int) {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFL, stream.flags); err != nil {
			t.Fatal(err)
		}
	})
	// Release only after the live sample, so exit-only restoration cannot mask it.
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	processFlagWait(t, command, -1, time.Second)
	if live == stream.flags {
		t.Fatal("live observer missed transient flag mutation")
	}
	stream.check(t)
}

func processFlagLive(t *testing.T, socket, nonblocking bool, action, alias string) {
	t.Helper()
	f := firstProcess(t, false)
	limit := 5 * time.Second
	args := []string{"query", "--connection", "missing", "--file", "-", "--timeout", "2s"}
	if action == "default-timeout" {
		limit = 35 * time.Second
		args = args[:len(args)-2]
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	in, out, diagnostic := newProcessFlagStream(t, socket, true, nonblocking), newProcessFlagStream(t, socket, false, nonblocking), newProcessFlagStream(t, socket, false, nonblocking)
	if alias == "shared-output" {
		diagnostic = out
	}
	if alias == "bidirectional" {
		out = in
	}
	command := f.command(ctx, args...)
	command.Stdin, command.Stdout, command.Stderr = in.source, out.source, diagnostic.source
	start := time.Now()
	processFlagStart(t, command)
	processFlagConsumed(t, command, in)
	for _, s := range []*processFlagStream{in, out, diagnostic} {
		s.check(t)
	}
	if action == "interrupt" || action == "terminate" {
		signal := syscall.SIGINT
		if action == "terminate" {
			signal = syscall.SIGTERM
		}
		if err := command.Process.Signal(signal); err != nil {
			t.Fatal(err)
		}
		_ = command.Process.Signal(signal)
	}
	processFlagWait(t, command, 6, limit)
	if time.Since(start) > limit {
		t.Fatal("process exceeded total deadline")
	}
	for _, s := range []*processFlagStream{in, out, diagnostic} {
		s.check(t)
	}
	output := processFlagDrain(t, out.peer)
	var diagnostics []byte
	if diagnostic == out {
		parts := bytes.SplitN(output, []byte{'\n'}, 2)
		if len(parts) != 2 {
			t.Fatal("shared output lost envelope")
		}
		output, diagnostics = append(parts[0], '\n'), parts[1]
	} else {
		diagnostics = processFlagDrain(t, diagnostic.peer)
	}
	doc := processDocument(t, output, diagnostics, 6)
	if doc["error"].(map[string]any)["code"] != "TIMEOUT" {
		t.Fatal("lost timeout classification")
	}
	processFlagReusable(t, in, true)
	processFlagReusable(t, out, false)
	if diagnostic != out {
		processFlagReusable(t, diagnostic, false)
	}
}

func processFlagBackpressure(t *testing.T, socket, nonblocking, canceled bool) {
	t.Helper()
	f := firstProcess(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	in, out, diagnostic := newProcessFlagStream(t, socket, true, nonblocking), newProcessFlagStream(t, socket, false, nonblocking), newProcessFlagStream(t, socket, false, nonblocking)
	out.fill(t)
	command := f.command(ctx, "capabilities", "--timeout", "2s")
	command.Stdin, command.Stdout, command.Stderr = in.source, out.source, diagnostic.source
	processFlagStart(t, command)
	if !processSignalReady(command.Process.Pid, time.Second) {
		t.Fatal("publication setup not ready")
	}
	for _, s := range []*processFlagStream{in, out, diagnostic} {
		s.check(t)
	}
	want := 7
	if canceled {
		_ = command.Process.Signal(syscall.SIGTERM)
		_ = command.Process.Signal(syscall.SIGTERM)
		want = 6
	}
	processFlagWait(t, command, want, 2*time.Second)
	for _, s := range []*processFlagStream{in, out, diagnostic} {
		s.check(t)
	}
	data := bytes.TrimLeft(processFlagDrain(t, out.peer), "x")
	// Publication may be absent or partial, but cannot retry a replacement JSON.
	decoder := json.NewDecoder(bytes.NewReader(data))
	var doc any
	if decoder.Decode(&doc) == nil && decoder.Decode(&doc) != io.EOF {
		t.Fatal("publication retried a replacement envelope")
	}
	processFlagDrain(t, diagnostic.peer)
	processFlagReusable(t, in, true)
	processFlagReusable(t, out, false)
	processFlagReusable(t, diagnostic, false)
}

func testProcessFlagAliasBarrier(t *testing.T) {
	stream := newProcessFlagStream(t, true, true, false)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// A matching output duplicate alone must not prove input acquisition.
	command := exec.CommandContext(ctx, "/bin/sh", "-c", "exec 3>&1; exec sleep 10")
	command.Stdin, command.Stdout, command.Stderr = stream.source, stream.source, os.Stderr
	processFlagStart(t, command)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		base := filepath.Join("/proc", strconv.Itoa(command.Process.Pid), "fd")
		input, _ := os.Readlink(filepath.Join(base, "0"))
		if duplicate, _ := os.Readlink(filepath.Join(base, "3")); input != "" && duplicate == input {
			if processInputAcquired(command.Process.Pid) {
				t.Fatal("output alias falsely proved input acquisition")
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("alias adversary did not establish output duplicate")
}

func testProcessFlagAcquisitionFailure(t *testing.T) {
	for _, target := range []string{"stdin", "stdout", "stderr"} {
		for _, closed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/closed=%t", target, closed), func(t *testing.T) {
				f := firstProcess(t, false)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				in, out, diagnostic := newProcessFlagStream(t, false, true, false), newProcessFlagStream(t, false, false, false), newProcessFlagStream(t, false, false, false)
				command := f.command(ctx, "capabilities")
				command.Stdin, command.Stdout, command.Stderr = in.source, out.source, diagnostic.source
				want := 7
				if target == "stderr" {
					want = 0
				}
				if closed {
					// Go's runtime checkfds replaces closed standard slots with
					// O_RDWR /dev/null before main; retain that existing behavior.
					want = 0
					script := map[string]string{"stdin": "exec 0<&-; exec \"$@\"", "stdout": "exec 1>&-; exec \"$@\"", "stderr": "exec 2>&-; exec \"$@\""}[target]
					command = exec.CommandContext(ctx, "/bin/sh", "-c", script, "sh", firstProcessBinary, "capabilities")
					command.Env = f.env
					command.Stdin, command.Stdout, command.Stderr = in.source, out.source, diagnostic.source
				} else {
					switch target {
					case "stdin":
						command.Stdin = out.source
					case "stdout":
						command.Stdout = in.source
					case "stderr":
						command.Stderr = in.source
					}
				}
				processFlagStart(t, command)
				processFlagWait(t, command, want, 5*time.Second)
				for _, s := range []*processFlagStream{in, out, diagnostic} {
					s.check(t)
				}
				if target != "stdout" {
					processDocument(t, processFlagDrain(t, out.peer), nil, want)
				}
			})
		}
	}
}
