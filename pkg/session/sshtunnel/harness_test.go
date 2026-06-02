package sshtunnel

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// fakeSecretPrompter is a canned-response session.SecretPrompter for tests.
// It records the hint it was last asked with.
type fakeSecretPrompter struct {
	value string
	err   error
	hint  string
	calls int
}

func (f *fakeSecretPrompter) PromptSecret(_ context.Context, hint string) (string, error) {
	f.hint = hint
	f.calls++
	return f.value, f.err
}

// genKey returns a fresh ed25519 ssh.Signer plus its PEM-encoded (unencrypted)
// OpenSSH private key.
func genKey(t *testing.T) (ssh.Signer, []byte) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return signer, pem.EncodeToMemory(block)
}

// genEncryptedKey returns an ed25519 ssh.Signer plus its PEM-encoded key
// encrypted with passphrase.
func genEncryptedKey(t *testing.T, passphrase string) (ssh.Signer, []byte) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	if err != nil {
		t.Fatalf("marshal encrypted key: %v", err)
	}
	return signer, pem.EncodeToMemory(block)
}

// testServer is an in-process SSH bastion. It authenticates a single
// authorized public key, presents a fixed host key, and forwards direct-tcpip
// channels to a backend echo listener.
type testServer struct {
	Addr      string // host:port the client should dial
	HostKey   ssh.Signer
	Backend   string // backend addr reachable through the tunnel
	listener  net.Listener
	backendLn net.Listener
}

// startServer launches a bastion authorizing authorizedKey, fronted by
// hostKey, plus a backend echo server. Both shut down via t.Cleanup.
func startServer(t *testing.T, hostKey ssh.Signer, authorizedKey ssh.PublicKey) *testServer {
	t.Helper()

	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("backend listen: %v", err)
	}
	go runEchoBackend(backendLn)

	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if string(key.Marshal()) == string(authorizedKey.Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, errUnauthorized
		},
	}
	cfg.AddHostKey(hostKey)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bastion listen: %v", err)
	}

	srv := &testServer{
		Addr:      ln.Addr().String(),
		HostKey:   hostKey,
		Backend:   backendLn.Addr().String(),
		listener:  ln,
		backendLn: backendLn,
	}
	go srv.serve(cfg)

	t.Cleanup(func() {
		_ = ln.Close()
		_ = backendLn.Close()
	})
	return srv
}

// startPasswordServer launches a bastion authorizing a single password,
// fronted by hostKey, plus a backend echo server. Both shut down via
// t.Cleanup.
func startPasswordServer(t *testing.T, hostKey ssh.Signer, password string) *testServer {
	t.Helper()

	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("backend listen: %v", err)
	}
	go runEchoBackend(backendLn)

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if string(pw) == password {
				return &ssh.Permissions{}, nil
			}
			return nil, errUnauthorized
		},
	}
	cfg.AddHostKey(hostKey)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bastion listen: %v", err)
	}

	srv := &testServer{
		Addr:      ln.Addr().String(),
		HostKey:   hostKey,
		Backend:   backendLn.Addr().String(),
		listener:  ln,
		backendLn: backendLn,
	}
	go srv.serve(cfg)

	t.Cleanup(func() {
		_ = ln.Close()
		_ = backendLn.Close()
	})
	return srv
}

var errUnauthorized = errors.New("unauthorized public key")

func (s *testServer) serve(cfg *ssh.ServerConfig) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn, cfg)
	}
}

func (s *testServer) handleConn(conn net.Conn, cfg *ssh.ServerConfig) {
	sc, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		_ = conn.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	go func() { _ = sc.Wait() }()

	for newChan := range chans {
		if newChan.ChannelType() != "direct-tcpip" {
			_ = newChan.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		go s.handleDirectTCPIP(newChan)
	}
}

func (s *testServer) handleDirectTCPIP(newChan ssh.NewChannel) {
	ch, reqs, err := newChan.Accept()
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)

	backend, err := net.Dial("tcp", s.Backend)
	if err != nil {
		_ = ch.Close()
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(backend, ch); closeWrite(backend) }()
	go func() { defer wg.Done(); _, _ = io.Copy(ch, backend); _ = ch.Close() }()
	wg.Wait()
	_ = backend.Close()
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}

// runEchoBackend echoes every byte received back to the sender.
func runEchoBackend(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			_, _ = io.Copy(c, c)
			_ = c.Close()
		}(conn)
	}
}
