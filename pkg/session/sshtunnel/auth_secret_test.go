package sshtunnel

import (
	"context"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/session"
)

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
