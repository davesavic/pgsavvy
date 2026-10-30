//go:build linux || darwin

package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func temporaryPassfileTarget(t *testing.T, content string, mode os.FileMode) (TemporaryTarget, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "SECRET-passfile")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	fields := temporaryFields()
	fields.Passfile = path
	target, err := NewTemporaryTarget(fields)
	if err != nil {
		t.Fatal(err)
	}
	return target, path
}

func TestTemporaryPassfile_Conventions(t *testing.T) {
	temporaryEnvironment(t)
	for _, tc := range []struct{ name, content, host, socket, database, username, want string }{
		{"exact", "127.0.0.1:5432:database:username:password\n", "", "", "", "", "password"},
		{"first-match", "*:*:*:*:first\n127.0.0.1:5432:database:username:second\n", "", "", "", "", "first"},
		{"wildcards", "# comment\r\n\r\nother:5432:*:*:wrong\r\n127.0.0.1:*:*:*: wild \\:pass\\\\word \r\n", "", "", "", "", " wild :pass\\word "},
		{"escaped-selectors", "*:*:data\\:base:user\\\\name:pass\\:word", "", "", "data:base", "user\\name", "pass:word"},
		{"tcp-canonical", "127.0.0.1:5432:*:*:canonical", "::ffff:127.0.0.1", "", "", "", "canonical"},
		{"ipv6", "\\:\\:1:5432:*:*:ipv6", "0:0:0:0:0:0:0:1", "", "", "", "ipv6"},
		{"socket-localhost", "localhost:5432:*:*:socket", "", "/tmp/socket", "", "", "socket"},
		{"missing-match", "other:5432:*:*:secret", "", "", "", "", ""},
		{"tcp-not-localhost", "localhost:5432:*:*:secret", "", "", "", "", ""},
		{"socket-not-ip", "127.0.0.1:5432:*:*:secret", "", "/tmp/socket", "", "", ""},
		{"empty-first-no-fallback", "*:*:*:*:\n*:*:*:*:second", "", "", "", "", ""},
		{"malformed", "*:*:*:missing\n*:*:*:*:too:many", "", "", "", "", ""},
		{"nul-password", "*:*:*:*:secret\x00tail", "", "", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, path := temporaryPassfileTarget(t, tc.content, 0o600)
			fields := temporaryFields()
			fields.Passfile, fields.Host, fields.SocketDir = path, tc.host, tc.socket
			if tc.database != "" {
				fields.Database = tc.database
			}
			if tc.username != "" {
				fields.Username = tc.username
			}
			target, err := NewTemporaryTarget(fields)
			if err != nil {
				t.Fatal(err)
			}
			password, err := ResolveTemporaryMachinePassword(context.Background(), target)
			if password != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("match result: password match=%t, err=%v", password == tc.want, err)
			}
			if err != nil && err.Error() != "CREDENTIALS_UNAVAILABLE" {
				t.Fatalf("unsafe error: %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != tc.content {
				t.Fatal("passfile mutated")
			}
		})
	}
}

type temporaryOwnerInfo struct {
	os.FileInfo
	owner syscall.Stat_t
}

func (i temporaryOwnerInfo) Sys() any { return &i.owner }

func TestTemporaryPassfile_ProtectionAndBound(t *testing.T) {
	temporaryEnvironment(t)
	for _, mode := range []os.FileMode{0o400, 0o600, 0o700, 0o640, 0o604, 0o777} {
		t.Run(fmt.Sprintf("%o", mode), func(t *testing.T) {
			target, path := temporaryPassfileTarget(t, "*:*:*:*:SECRET", mode)
			_, err := ResolveTemporaryMachinePassword(context.Background(), target)
			if (err == nil) != (mode&0o077 == 0) {
				t.Fatalf("mode accepted incorrectly: %v", err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			foreign := temporaryOwnerInfo{FileInfo: info, owner: syscall.Stat_t{Uid: uint32(os.Geteuid()) + 1}}
			if temporaryPassfileProtected(foreign, os.Geteuid()) {
				t.Fatal("foreign owner accepted")
			}
		})
	}
	for _, kind := range []string{"symlink", "directory", "fifo", "missing", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			target, path := temporaryPassfileTarget(t, "*:*:*:*:SECRET", 0o600)
			selected := path + "-" + kind
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(path, selected)
			case "directory":
				err = os.Mkdir(selected, 0o700)
			case "fifo":
				err = unix.Mkfifo(selected, 0o600)
			case "oversize":
				err = os.WriteFile(selected, []byte(strings.Repeat("x", temporaryPassfileMaxBytes+1)), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			target.passfile = selected
			password, err := ResolveTemporaryMachinePassword(context.Background(), target)
			if password != "" || err == nil || err.Error() != "CREDENTIALS_UNAVAILABLE" {
				t.Fatalf("unsafe %s acceptance/error: %v", kind, err)
			}
		})
	}
	content := "#" + strings.Repeat("x", temporaryPassfileMaxBytes-len("#\n*:*:*:*:boundary")) + "\n*:*:*:*:boundary"
	target, _ := temporaryPassfileTarget(t, content, 0o600)
	if password, err := ResolveTemporaryMachinePassword(context.Background(), target); err != nil || password != "boundary" {
		t.Fatalf("exact size boundary rejected: %v", err)
	}
}

func TestTemporaryPassfile_SameHandleAndCancellation(t *testing.T) {
	temporaryEnvironment(t)
	target, path := temporaryPassfileTarget(t, "*:*:*:*:original", 0o700)
	opens := 0
	var handle *os.File
	password, err := resolveTemporaryMachinePassword(context.Background(), target, func(selected string) (*os.File, error) {
		opens++
		if selected != path {
			t.Fatal("wrong credential source")
		}
		file, err := openTemporaryPassfile(selected)
		if err != nil {
			return nil, err
		}
		handle = file
		flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
		if err != nil || flags&unix.O_NONBLOCK == 0 {
			t.Fatal("credential handle is blocking")
		}
		fdflags, err := unix.FcntlInt(file.Fd(), unix.F_GETFD, 0)
		if err != nil || fdflags&unix.FD_CLOEXEC == 0 {
			t.Fatal("credential handle can leak into a child")
		}
		if err := os.Rename(path, path+"-original"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("*:*:*:*:replacement"), 0o666); err != nil {
			t.Fatal(err)
		}
		return file, nil
	})
	if err != nil || password != "original" || opens != 1 {
		t.Fatalf("reopened or followed replacement: opens=%d err=%v", opens, err)
	}
	if _, err := handle.Stat(); err == nil {
		t.Fatal("credential handle leaked")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = resolveTemporaryMachinePassword(ctx, target, func(string) (*os.File, error) { t.Fatal("precancelled open"); return nil, nil })
	if err == nil || err.Error() != "TIMEOUT" {
		t.Fatalf("precancelled credential: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	_, err = resolveTemporaryMachinePassword(ctx, target, func(string) (*os.File, error) {
		file, err := openTemporaryPassfile(path + "-original")
		cancel()
		return file, err
	})
	if err == nil || err.Error() != "TIMEOUT" {
		t.Fatalf("cancelled read: %v", err)
	}
	if _, err := matchTemporaryPassfile(ctx, target, "*:*:*:*:secret"); err == nil || err.Error() != "TIMEOUT" {
		t.Fatalf("cancelled matcher: %v", err)
	}
}

type temporaryCheckpointContext struct {
	context.Context
	check func() error
}

func (c temporaryCheckpointContext) Err() error { return c.check() }

func TestTemporaryPassfile_GrowingFileAndCheckpointCancellation(t *testing.T) {
	temporaryEnvironment(t)
	for _, cancelDuringRead := range []bool{false, true} {
		target, path := temporaryPassfileTarget(t, "*:*:*:*:original", 0o600)
		checks := 0
		ctx := temporaryCheckpointContext{Context: context.Background(), check: func() error {
			checks++
			// The first check precedes open; the second follows same-handle Stat.
			if checks == 2 {
				file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, err = file.WriteString(strings.Repeat("x", temporaryPassfileMaxBytes))
				closeErr := file.Close()
				if err != nil || closeErr != nil {
					t.Fatalf("growth fixture: %v %v", err, closeErr)
				}
			}
			if cancelDuringRead && checks >= 4 {
				return context.Canceled
			}
			return nil
		}}
		password, err := ResolveTemporaryMachinePassword(ctx, target)
		want := "CREDENTIALS_UNAVAILABLE"
		if cancelDuringRead {
			want = "TIMEOUT"
		}
		if password != "" || err == nil || err.Error() != want || checks < 4 {
			t.Fatalf("growing/cancelled read accepted: checks=%d err=%v", checks, err)
		}
	}
	fields := temporaryFields()
	target, err := NewTemporaryTarget(fields)
	if err != nil {
		t.Fatal(err)
	}
	checks := 0
	ctx := temporaryCheckpointContext{Context: context.Background(), check: func() error {
		checks++
		if checks >= 3 {
			return context.Canceled
		}
		return nil
	}}
	// Escapes crossing checkpoint boundaries must not skip cancellation checks.
	_, err = matchTemporaryPassfile(ctx, target, "*:*:*:*:"+strings.Repeat(`\\`, 32<<10))
	if err == nil || err.Error() != "TIMEOUT" || checks != 3 {
		t.Fatalf("long escaped-line cancellation: checks=%d err=%v", checks, err)
	}
}
