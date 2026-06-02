package sshtunnel

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// hostAddr splits "host:port" and returns the host.
func hostAddr(t *testing.T, addr string) string {
	t.Helper()
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split host: %v", err)
	}
	return host
}

// portAddr splits "host:port" and returns the port as an int.
func portAddr(t *testing.T, addr string) int {
	t.Helper()
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split port: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("atoi port: %v", err)
	}
	return port
}

// contains reports whether s contains substr.
func contains(s, substr string) bool { return strings.Contains(s, substr) }

// writeIdentity writes pem to a temp file and returns its path.
func writeIdentity(t *testing.T, pem []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, pem, 0o600); err != nil {
		t.Fatalf("write identity: %v", err)
	}
	return path
}

// freshKnownHosts returns a path to an empty (non-existent) known_hosts inside
// a temp dir, exercising the create-on-missing path.
func freshKnownHosts(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "known_hosts")
}

func TestOpenHappyPath(t *testing.T) {
	hostKey, _ := genKey(t)
	clientSigner, clientPEM := genKey(t)

	srv := startServer(t, hostKey, clientSigner.PublicKey())
	idFile := writeIdentity(t, clientPEM)

	cfg := models.SSHTunnelConfig{
		Host:         hostAddr(t, srv.Addr),
		Port:         portAddr(t, srv.Addr),
		User:         "tester",
		IdentityFile: idFile,
		KnownHosts:   freshKnownHosts(t),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tun, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = tun.Close() }()

	conn, err := tun.DialContext(ctx, "tcp", srv.Backend)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer func() { _ = conn.Close() }()

	want := "hello tunnel\n"
	if _, err := conn.Write([]byte(want)); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != want {
		t.Fatalf("echo = %q, want %q", got, want)
	}
}

func TestOpenHomeUnsetExpansion(t *testing.T) {
	t.Setenv("HOME", "")
	// On some platforms os.UserHomeDir reads other vars; clear the obvious ones.
	t.Setenv("USERPROFILE", "")

	cfg := models.SSHTunnelConfig{
		Host:         "127.0.0.1",
		User:         "tester",
		IdentityFile: "~/x",
		KnownHosts:   freshKnownHosts(t),
	}

	_, err := Open(context.Background(), cfg)
	if err == nil {
		t.Fatal("Open: expected error, got nil")
	}
	if !IsDialError(err) {
		t.Fatalf("Open err = %v, want *DialError", err)
	}
}

func TestOpenWrongKey(t *testing.T) {
	hostKey, _ := genKey(t)
	authorized, _ := genKey(t)
	_, wrongPEM := genKey(t) // a different, unauthorized key

	srv := startServer(t, hostKey, authorized.PublicKey())
	idFile := writeIdentity(t, wrongPEM)

	cfg := models.SSHTunnelConfig{
		Host:         hostAddr(t, srv.Addr),
		Port:         portAddr(t, srv.Addr),
		User:         "tester",
		IdentityFile: idFile,
		KnownHosts:   freshKnownHosts(t),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := Open(ctx, cfg)
	if err == nil {
		t.Fatal("Open: expected auth failure, got nil")
	}
	if !IsDialError(err) {
		t.Fatalf("Open err = %v, want *DialError", err)
	}
}

func TestOpenAgentMissingSock(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")

	cfg := models.SSHTunnelConfig{
		Host:              "127.0.0.1",
		User:              "tester",
		IdentityFromAgent: true,
		KnownHosts:        freshKnownHosts(t),
	}

	_, err := Open(context.Background(), cfg)
	if err == nil {
		t.Fatal("Open: expected error, got nil")
	}
	if !IsDialError(err) {
		t.Fatalf("Open err = %v, want *DialError", err)
	}
	if !contains(err.Error(), "ssh-agent") {
		t.Fatalf("err = %q, want mention of ssh-agent", err.Error())
	}
}

func TestTOFUPersistAndMismatch(t *testing.T) {
	hostKey, _ := genKey(t)
	clientSigner, clientPEM := genKey(t)
	authorized := clientSigner.PublicKey()

	srv := startServer(t, hostKey, authorized)
	idFile := writeIdentity(t, clientPEM)
	knownHosts := freshKnownHosts(t)

	cfg := models.SSHTunnelConfig{
		Host:         hostAddr(t, srv.Addr),
		Port:         portAddr(t, srv.Addr),
		User:         "tester",
		IdentityFile: idFile,
		KnownHosts:   knownHosts,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tun, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	_ = tun.Close()

	// known_hosts must now contain a persisted line.
	data, err := os.ReadFile(knownHosts)
	if err != nil {
		t.Fatalf("read known_hosts: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("known_hosts empty after TOFU; host key not persisted")
	}

	// Simulate the bastion's host key CHANGING: overwrite known_hosts with an
	// entry for the SAME host:port but a DIFFERENT key. Reconnecting to the
	// still-running server (which presents the original key) must now be
	// rejected as a host-key mismatch.
	otherKey, _ := genKey(t)
	hostPort := net.JoinHostPort(hostAddr(t, srv.Addr), strconv.Itoa(portAddr(t, srv.Addr)))
	line := knownhosts.Line([]string{hostPort}, otherKey.PublicKey())
	if err := os.WriteFile(knownHosts, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("rewrite known_hosts: %v", err)
	}

	_, err = Open(ctx, cfg)
	if err == nil {
		t.Fatal("second Open: expected host-key mismatch, got nil")
	}
	if !IsDialError(err) {
		t.Fatalf("mismatch err = %v, want *DialError", err)
	}
	if !contains(err.Error(), "mismatch") {
		t.Fatalf("err = %q, want host key mismatch", err.Error())
	}
}

func TestDialContextConcurrent(t *testing.T) {
	hostKey, _ := genKey(t)
	clientSigner, clientPEM := genKey(t)

	srv := startServer(t, hostKey, clientSigner.PublicKey())
	idFile := writeIdentity(t, clientPEM)

	cfg := models.SSHTunnelConfig{
		Host:         hostAddr(t, srv.Addr),
		Port:         portAddr(t, srv.Addr),
		User:         "tester",
		IdentityFile: idFile,
		KnownHosts:   freshKnownHosts(t),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tun, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = tun.Close() }()

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			conn, err := tun.DialContext(ctx, "tcp", srv.Backend)
			if err != nil {
				errs[idx] = err
				return
			}
			defer func() { _ = conn.Close() }()
			msg := []byte("ping\n")
			if _, err := conn.Write(msg); err != nil {
				errs[idx] = err
				return
			}
			buf := make([]byte, len(msg))
			if _, err := conn.Read(buf); err != nil {
				errs[idx] = err
			}
		}(i)
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Fatalf("goroutine %d: %v", i, e)
		}
	}
}

func TestCloseIdempotent(t *testing.T) {
	hostKey, _ := genKey(t)
	clientSigner, clientPEM := genKey(t)

	srv := startServer(t, hostKey, clientSigner.PublicKey())
	idFile := writeIdentity(t, clientPEM)

	cfg := models.SSHTunnelConfig{
		Host:         hostAddr(t, srv.Addr),
		Port:         portAddr(t, srv.Addr),
		User:         "tester",
		IdentityFile: idFile,
		KnownHosts:   freshKnownHosts(t),
	}

	tun, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if err := tun.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	// Second close must not panic and must be safe.
	_ = tun.Close()
}

func TestEncryptedKeyPassphraseCommand(t *testing.T) {
	const passphrase = "correctpass"
	hostKey, _ := genKey(t)
	clientSigner, clientPEM := genEncryptedKey(t, passphrase)

	srv := startServer(t, hostKey, clientSigner.PublicKey())
	idFile := writeIdentity(t, clientPEM)

	cfg := models.SSHTunnelConfig{
		Host:              hostAddr(t, srv.Addr),
		Port:              portAddr(t, srv.Addr),
		User:              "tester",
		IdentityFile:      idFile,
		PassphraseCommand: "printf %s " + passphrase,
		KnownHosts:        freshKnownHosts(t),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tun, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open with encrypted key: %v", err)
	}
	_ = tun.Close()
}

func TestEncryptedKeyNoPassphraseCommand(t *testing.T) {
	_, clientPEM := genEncryptedKey(t, "secret")
	idFile := writeIdentity(t, clientPEM)

	cfg := models.SSHTunnelConfig{
		Host:         "127.0.0.1",
		Port:         2222,
		User:         "tester",
		IdentityFile: idFile,
		KnownHosts:   freshKnownHosts(t),
	}

	_, err := Open(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error for encrypted key without passphrase_command")
	}
	if !IsDialError(err) {
		t.Fatalf("err = %v, want *DialError", err)
	}
	if !contains(err.Error(), "passphrase_command") {
		t.Fatalf("err = %q, want passphrase_command guidance", err.Error())
	}
}

func TestEncryptedKeyWrongCommand(t *testing.T) {
	hostKey, _ := genKey(t)
	clientSigner, clientPEM := genEncryptedKey(t, "correctpass")
	srv := startServer(t, hostKey, clientSigner.PublicKey())
	idFile := writeIdentity(t, clientPEM)

	cfg := models.SSHTunnelConfig{
		Host:              hostAddr(t, srv.Addr),
		Port:              portAddr(t, srv.Addr),
		User:              "tester",
		IdentityFile:      idFile,
		PassphraseCommand: "printf wrongpass",
		KnownHosts:        freshKnownHosts(t),
	}

	_, err := Open(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected decrypt failure with wrong passphrase")
	}
	if !IsDialError(err) {
		t.Fatalf("err = %v, want *DialError", err)
	}
}

func TestNoAuthConfigured(t *testing.T) {
	cfg := models.SSHTunnelConfig{
		Host:       "127.0.0.1",
		User:       "tester",
		KnownHosts: freshKnownHosts(t),
	}
	_, err := Open(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error when no auth configured")
	}
	if !IsDialError(err) {
		t.Fatalf("err = %v, want *DialError", err)
	}
}
