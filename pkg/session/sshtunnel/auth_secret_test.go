package sshtunnel

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
)

type reviewAgentSocket struct {
	accepted  chan struct{}
	closed    chan struct{}
	requested chan struct{}
	peer      net.Conn
}

func reviewWaitAgent(t *testing.T, done <-chan struct{}, phase string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", phase)
	}
}

func reviewStartAgent(t *testing.T, pem []byte, blocked bool) *reviewAgentSocket {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix agent socket regression requires a Unix socket platform")
	}
	dir, err := os.MkdirTemp("", "pgsavvy-agent-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(dir); err != nil {
			t.Errorf("remove owned agent directory: %v", err)
		}
	})
	sock := filepath.Join(dir, "sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.ParseRawPrivateKey(pem)
	if err != nil {
		_ = ln.Close()
		t.Fatal("parse synthetic agent key")
	}
	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: key}); err != nil {
		_ = ln.Close()
		t.Fatal("add synthetic agent key")
	}
	f := &reviewAgentSocket{accepted: make(chan struct{}), closed: make(chan struct{}), requested: make(chan struct{})}
	go func() {
		defer close(f.closed)
		conn, err := ln.Accept()
		f.peer = conn
		close(f.accepted)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		if blocked {
			var first [1]byte
			if _, err := io.ReadFull(conn, first[:]); err != nil {
				return
			}
			close(f.requested)
			_, _ = io.Copy(io.Discard, conn)
			return
		}
		_ = agent.ServeAgent(keyring, conn)
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		reviewWaitAgent(t, f.accepted, "agent accept teardown")
		if f.peer != nil {
			_ = f.peer.Close()
		}
		reviewWaitAgent(t, f.closed, "agent handler teardown")
	})
	t.Setenv("SSH_AUTH_SOCK", sock)
	return f
}

func TestReview_agent_failure_cleanup(t *testing.T) {
	_, pem := genKey(t)
	_, encrypted := genEncryptedKey(t, "fixture-passphrase")
	for _, phase := range []string{"identity read", "identity parse", "missing passphrase", "wrong passphrase", "prompt cancel", "passphrase command", "password command", "host key preparation", "tcp dial", "host key mismatch", "authentication", "cancelled dial"} {
		t.Run(phase, func(t *testing.T) {
			f := reviewStartAgent(t, pem, false)
			cfg := models.SSHTunnelConfig{Host: "127.0.0.1", Port: 1, User: "tester", IdentityFromAgent: true, KnownHosts: freshKnownHosts(t)}
			var prompter session.SecretPrompter
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			const secret = "agent-fixture-secret"
			switch phase {
			case "identity read":
				cfg.IdentityFile = filepath.Join(t.TempDir(), "missing")
			case "identity parse":
				cfg.IdentityFile = writeIdentity(t, []byte("invalid key"))
			case "missing passphrase":
				cfg.IdentityFile = writeIdentity(t, encrypted)
			case "wrong passphrase":
				cfg.IdentityFile = writeIdentity(t, encrypted)
				prompter = &fakeSecretPrompter{value: secret}
			case "prompt cancel":
				cfg.IdentityFile = writeIdentity(t, encrypted)
				prompter = &fakeSecretPrompter{err: session.NewSecretPromptCancelled(nil)}
			case "password command":
				cfg.SSHPasswordCommand = "printf %s " + secret + "; printf %s " + secret + " >&2; exit 1"
			case "passphrase command":
				cfg.IdentityFile = writeIdentity(t, encrypted)
				cfg.PassphraseCommand = "printf %s " + secret + "; printf %s " + secret + " >&2; exit 1"
			case "host key preparation":
				if err := os.WriteFile(cfg.KnownHosts, []byte("invalid known host line\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "tcp dial":
				ln, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				cfg.Port = portAddr(t, ln.Addr().String())
				_ = ln.Close()
			case "host key mismatch", "authentication":
				hostKey, _ := genKey(t)
				wrongKey, _ := genKey(t)
				srv := startServer(t, hostKey, wrongKey.PublicKey())
				cfg.Port = portAddr(t, srv.Addr)
				if phase == "host key mismatch" {
					line := knownhosts.Line([]string{srv.Addr}, wrongKey.PublicKey())
					if err := os.WriteFile(cfg.KnownHosts, []byte(line+"\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			case "cancelled dial":
				cancel()
			}
			var log bytes.Buffer
			SetGlobalLogger(slog.New(slog.NewJSONHandler(&log, nil)))
			t.Cleanup(func() { SetGlobalLogger(nil) })
			tun, err := OpenWithPrompter(ctx, ctx, cfg, prompter)
			if tun != nil {
				_ = tun.Close()
				t.Fatal("unexpected tunnel from failed open")
			}
			if !IsDialError(err) {
				t.Fatalf("expected typed open failure, got %v", err)
			}
			if contains(err.Error(), secret) || contains(log.String(), secret) {
				t.Fatal("synthetic credential leaked in error or telemetry")
			}
			reviewWaitAgent(t, f.accepted, "agent connection")
			reviewWaitAgent(t, f.closed, "failed-open agent EOF before fixture cleanup")
		})
	}
}

func TestReview_agent_lifetime(t *testing.T) {
	hostKey, _ := genKey(t)
	clientKey, pem := genKey(t)
	f := reviewStartAgent(t, pem, false)
	srv := startServer(t, hostKey, clientKey.PublicKey())
	cfg := models.SSHTunnelConfig{Host: hostAddr(t, srv.Addr), Port: portAddr(t, srv.Addr), User: "tester", IdentityFromAgent: true, KnownHosts: freshKnownHosts(t)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tun, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tun.Close() })
	reviewWaitAgent(t, f.accepted, "successful agent connection")
	cancel()
	// The opening context must no longer own an established tunnel's agent.
	select {
	case <-f.closed:
		t.Fatal("agent closed before tunnel shutdown")
	default:
	}
	echoes(t, tun, srv.Backend)
	if err := tun.Close(); err != nil {
		t.Fatal(err)
	}
	reviewWaitAgent(t, f.closed, "successful-close agent EOF before fixture cleanup")
	if err := tun.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReview_agent_cancel_blocked_request(t *testing.T) {
	hostKey, _ := genKey(t)
	clientKey, pem := genKey(t)
	f := reviewStartAgent(t, pem, true)
	srv := startServer(t, hostKey, clientKey.PublicKey())
	cfg := models.SSHTunnelConfig{Host: hostAddr(t, srv.Addr), Port: portAddr(t, srv.Addr), User: "tester", IdentityFromAgent: true, KnownHosts: freshKnownHosts(t)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		tun, err := Open(ctx, cfg)
		if tun != nil {
			_ = tun.Close()
		}
		done <- err
	}()
	reviewWaitAgent(t, f.requested, "blocked agent signer request")
	cancel()
	select {
	case err := <-done:
		if !IsDialError(err) || !errors.Is(err, context.Canceled) {
			t.Fatalf("expected typed cancellation, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Open did not settle after cancellation")
	}
	reviewWaitAgent(t, f.closed, "cancelled agent EOF before fixture cleanup")
}

func TestReview_agent_auth_ownership(t *testing.T) {
	_, pem := genKey(t)
	f := reviewStartAgent(t, pem, false)
	cfg := models.SSHTunnelConfig{IdentityFromAgent: true, IdentityFile: writeIdentity(t, pem), SSHPasswordCommand: "printf fixture-password"}
	methods, selected, owned, err := authMethods(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if owned == nil {
		t.Fatal("prepared auth did not expose its agent owner")
	}
	t.Cleanup(func() {
		if owned != nil {
			_ = owned.Close()
		}
	})
	if len(methods) != 3 || !slices.Equal(selected, []string{"agent", "identity", "password"}) {
		t.Fatalf("auth order = %v, want agent/identity/password", selected)
	}
	if err := owned.Close(); err != nil {
		t.Fatal(err)
	}
	owned = nil
	reviewWaitAgent(t, f.closed, "prepared owner close")
}

func TestReview_agent_cancel_stalled_bastion(t *testing.T) {
	_, pem := genKey(t)
	f := reviewStartAgent(t, pem, false)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted, closed := make(chan struct{}), make(chan struct{})
	var peer net.Conn
	go func() {
		defer close(closed)
		conn, err := ln.Accept()
		peer = conn
		close(accepted)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = io.Copy(io.Discard, conn)
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		reviewWaitAgent(t, accepted, "bastion accept teardown")
		if peer != nil {
			_ = peer.Close()
		}
		reviewWaitAgent(t, closed, "bastion handler teardown")
	})
	cfg := models.SSHTunnelConfig{Host: hostAddr(t, ln.Addr().String()), Port: portAddr(t, ln.Addr().String()), User: "tester", IdentityFromAgent: true, KnownHosts: freshKnownHosts(t)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		tun, err := Open(ctx, cfg)
		if tun != nil {
			_ = tun.Close()
		}
		done <- err
	}()
	reviewWaitAgent(t, accepted, "stalled bastion connection")
	cancel()
	select {
	case err := <-done:
		if !IsDialError(err) || !errors.Is(err, context.Canceled) {
			t.Fatalf("expected typed cancellation, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Open did not settle after cancellation")
	}
	reviewWaitAgent(t, closed, "cancelled bastion EOF before fixture cleanup")
	reviewWaitAgent(t, f.closed, "stalled-handshake agent EOF before fixture cleanup")
}

// echoes confirms the tunnel forwards bytes end-to-end to the backend.
func echoes(t *testing.T, tun *Tunnel, backend string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := tun.DialContext(ctx, "tcp", backend)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer func() { _ = conn.Close() }()
	want := "ping\n"
	if _, err := conn.Write([]byte(want)); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(want))
	if _, err := conn.Read(buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != want {
		t.Fatalf("echo = %q, want %q", string(buf), want)
	}
}

func TestEncryptedKeyInteractivePassphrase(t *testing.T) {
	const passphrase = "correctpass"
	hostKey, _ := genKey(t)
	clientSigner, clientPEM := genEncryptedKey(t, passphrase)

	srv := startServer(t, hostKey, clientSigner.PublicKey())
	idFile := writeIdentity(t, clientPEM)

	cfg := models.SSHTunnelConfig{
		Host:         hostAddr(t, srv.Addr),
		Port:         portAddr(t, srv.Addr),
		User:         "tester",
		IdentityFile: idFile,
		KnownHosts:   freshKnownHosts(t),
	}

	prompter := &fakeSecretPrompter{value: passphrase}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tun, err := OpenWithPrompter(ctx, ctx, cfg, prompter)
	if err != nil {
		t.Fatalf("OpenWithPrompter: %v", err)
	}
	defer func() { _ = tun.Close() }()

	if prompter.calls != 1 {
		t.Fatalf("prompter calls = %d, want 1", prompter.calls)
	}
	echoes(t, tun, srv.Backend)
}

func TestEncryptedKeyInteractiveWrongPassphrase(t *testing.T) {
	hostKey, _ := genKey(t)
	clientSigner, clientPEM := genEncryptedKey(t, "correctpass")
	srv := startServer(t, hostKey, clientSigner.PublicKey())
	idFile := writeIdentity(t, clientPEM)

	cfg := models.SSHTunnelConfig{
		Host:         hostAddr(t, srv.Addr),
		Port:         portAddr(t, srv.Addr),
		User:         "tester",
		IdentityFile: idFile,
		KnownHosts:   freshKnownHosts(t),
	}

	const secret = "wrongpass-supersecret"
	prompter := &fakeSecretPrompter{value: secret}

	_, err := OpenWithPrompter(context.Background(), context.Background(), cfg, prompter)
	if err == nil {
		t.Fatal("expected decrypt failure with wrong passphrase")
	}
	if !IsDialError(err) {
		t.Fatalf("err = %v, want *DialError", err)
	}
	if !contains(err.Error(), "identity") {
		t.Fatalf("err = %q, want mention of identity/ssh auth", err.Error())
	}
	if contains(err.Error(), secret) {
		t.Fatalf("err leaked secret: %q", err.Error())
	}
}

func TestEncryptedKeyEmptyInteractivePassphrase(t *testing.T) {
	hostKey, _ := genKey(t)
	clientSigner, clientPEM := genEncryptedKey(t, "correctpass")
	srv := startServer(t, hostKey, clientSigner.PublicKey())
	idFile := writeIdentity(t, clientPEM)

	cfg := models.SSHTunnelConfig{
		Host:         hostAddr(t, srv.Addr),
		Port:         portAddr(t, srv.Addr),
		User:         "tester",
		IdentityFile: idFile,
		KnownHosts:   freshKnownHosts(t),
	}

	prompter := &fakeSecretPrompter{value: ""}

	_, err := OpenWithPrompter(context.Background(), context.Background(), cfg, prompter)
	if err == nil {
		t.Fatal("expected auth failure with empty passphrase")
	}
	if !IsDialError(err) {
		t.Fatalf("err = %v, want *DialError", err)
	}
}

func TestEncryptedKeyNilPrompterNoCommand(t *testing.T) {
	_, clientPEM := genEncryptedKey(t, "secret")
	idFile := writeIdentity(t, clientPEM)

	cfg := models.SSHTunnelConfig{
		Host:         "127.0.0.1",
		Port:         2222,
		User:         "tester",
		IdentityFile: idFile,
		KnownHosts:   freshKnownHosts(t),
	}

	_, err := OpenWithPrompter(context.Background(), context.Background(), cfg, nil)
	if err == nil {
		t.Fatal("expected error for encrypted key, nil prompter, no command")
	}
	if !IsDialError(err) {
		t.Fatalf("err = %v, want *DialError", err)
	}
	if !contains(err.Error(), "passphrase_command") {
		t.Fatalf("err = %q, want passphrase_command guidance", err.Error())
	}
}

func TestPasswordAuthInteractive(t *testing.T) {
	const password = "hunter2"
	hostKey, _ := genKey(t)
	srv := startPasswordServer(t, hostKey, password)

	cfg := models.SSHTunnelConfig{
		Host:       hostAddr(t, srv.Addr),
		Port:       portAddr(t, srv.Addr),
		User:       "tester",
		KnownHosts: freshKnownHosts(t),
	}

	prompter := &fakeSecretPrompter{value: password}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tun, err := OpenWithPrompter(ctx, ctx, cfg, prompter)
	if err != nil {
		t.Fatalf("OpenWithPrompter: %v", err)
	}
	defer func() { _ = tun.Close() }()

	if prompter.calls != 1 {
		t.Fatalf("prompter calls = %d, want 1", prompter.calls)
	}
	echoes(t, tun, srv.Backend)
}

func TestPasswordAuthInteractiveWrong(t *testing.T) {
	hostKey, _ := genKey(t)
	srv := startPasswordServer(t, hostKey, "hunter2")

	cfg := models.SSHTunnelConfig{
		Host:       hostAddr(t, srv.Addr),
		Port:       portAddr(t, srv.Addr),
		User:       "tester",
		KnownHosts: freshKnownHosts(t),
	}

	const secret = "wrongpw-supersecret"
	prompter := &fakeSecretPrompter{value: secret}

	_, err := OpenWithPrompter(context.Background(), context.Background(), cfg, prompter)
	if err == nil {
		t.Fatal("expected auth failure with wrong password")
	}
	if !IsDialError(err) {
		t.Fatalf("err = %v, want *DialError", err)
	}
	if contains(err.Error(), secret) {
		t.Fatalf("err leaked secret: %q", err.Error())
	}
}

func TestPasswordAuthCommand(t *testing.T) {
	const password = "hunter2"
	hostKey, _ := genKey(t)
	srv := startPasswordServer(t, hostKey, password)

	cfg := models.SSHTunnelConfig{
		Host:               hostAddr(t, srv.Addr),
		Port:               portAddr(t, srv.Addr),
		User:               "tester",
		SSHPasswordCommand: "printf %s " + password,
		KnownHosts:         freshKnownHosts(t),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Command path must work even with a nil prompter.
	tun, err := OpenWithPrompter(ctx, ctx, cfg, nil)
	if err != nil {
		t.Fatalf("OpenWithPrompter: %v", err)
	}
	defer func() { _ = tun.Close() }()

	echoes(t, tun, srv.Backend)
}

func TestPasswordAuthCommandFails(t *testing.T) {
	hostKey, _ := genKey(t)
	srv := startPasswordServer(t, hostKey, "hunter2")

	const secret = "leakable-pw"
	cfg := models.SSHTunnelConfig{
		Host: hostAddr(t, srv.Addr),
		Port: portAddr(t, srv.Addr),
		User: "tester",
		// Emit the resolved password on stdout AND echo it to stderr, then
		// fail: ExecPasswordCommand must scrub the password out of stderr.
		SSHPasswordCommand: "printf %s " + secret + "; printf %s " + secret + " >&2; exit 1",
		KnownHosts:         freshKnownHosts(t),
	}

	_, err := OpenWithPrompter(context.Background(), context.Background(), cfg, nil)
	if err == nil {
		t.Fatal("expected error when ssh_password_command exits non-zero")
	}
	if !IsDialError(err) {
		t.Fatalf("err = %v, want *DialError", err)
	}
	if !contains(err.Error(), "ssh_password_command") {
		t.Fatalf("err = %q, want mention of ssh_password_command", err.Error())
	}
	if contains(err.Error(), secret) {
		t.Fatalf("err leaked password: %q", err.Error())
	}
}

func TestPasswordAuthCancelled(t *testing.T) {
	hostKey, _ := genKey(t)
	srv := startPasswordServer(t, hostKey, "hunter2")

	cfg := models.SSHTunnelConfig{
		Host:       hostAddr(t, srv.Addr),
		Port:       portAddr(t, srv.Addr),
		User:       "tester",
		KnownHosts: freshKnownHosts(t),
	}

	prompter := &fakeSecretPrompter{err: session.NewSecretPromptCancelled(nil)}

	_, err := OpenWithPrompter(context.Background(), context.Background(), cfg, prompter)
	if err == nil {
		t.Fatal("expected error when prompter cancelled")
	}
	if !IsDialError(err) {
		t.Fatalf("err = %v, want *DialError", err)
	}
}
