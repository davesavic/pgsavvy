//go:build linux

package sshtunnel

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/davesavic/pgsavvy/pkg/models"
)

func TestMachineSSH_Auth(t *testing.T) {
	host, _ := genKey(t)
	identity, pem := genKey(t)
	srv := startServer(t, host, identity.PublicKey())
	cfg := machineSSHProfile(t, srv, pem)
	for _, state := range []string{"none", "missing-key", "bad-key", "empty-secret", "oversize-secret", "helper-error"} {
		t.Run(state, func(t *testing.T) {
			copy := cfg
			copy.IdentityFile = ""
			options := MachineOptions{ExecCredential: func(context.Context, string) (string, error) { return "", nil }}
			switch state {
			case "missing-key":
				copy.IdentityFile = filepath.Join(t.TempDir(), "missing")
			case "bad-key":
				copy.IdentityFile = writeIdentity(t, []byte("not-a-key"))
			case "empty-secret":
				copy.SSHPasswordCommand = "trusted"
			case "oversize-secret":
				copy.SSHPasswordCommand = "trusted"
				options.ExecCredential = func(context.Context, string) (string, error) { return strings.Repeat("x", (32<<10)+1), nil }
			case "helper-error":
				copy.SSHPasswordCommand = "trusted"
				options.ExecCredential = func(context.Context, string) (string, error) { return "", errors.New("SECRET") }
			}
			_, err := OpenMachine(context.Background(), copy, options)
			var safe *MachineError
			if !errors.As(err, &safe) || safe.Code != "CREDENTIALS_UNAVAILABLE" || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("auth %s: %v", state, err)
			}
		})
	}
	passwordServer := startPasswordServer(t, host, "secret")
	passwordCfg := machineSSHProfile(t, passwordServer, pem)
	passwordCfg.IdentityFile = ""
	passwordCfg.SSHPasswordCommand = "password-command"
	called := 0
	tunnel, err := OpenMachine(context.Background(), passwordCfg, MachineOptions{ExecCredential: func(ctx context.Context, command string) (string, error) {
		called++
		deadline, ok := ctx.Deadline()
		if command != "password-command" || !ok || time.Until(deadline) > 5*time.Second {
			t.Error("unbounded or wrong helper")
		}
		return "secret", nil
	}})
	if err != nil || called != 1 {
		t.Fatalf("password helper: %v calls=%d", err, called)
	}
	_ = tunnel.Close()
	encrypted, encryptedPEM := genEncryptedKey(t, "passphrase")
	encryptedServer := startServer(t, host, encrypted.PublicKey())
	encryptedCfg := machineSSHProfile(t, encryptedServer, encryptedPEM)
	encryptedCfg.PassphraseCommand = "passphrase-command"
	tunnel, err = OpenMachine(context.Background(), encryptedCfg, MachineOptions{ExecCredential: func(_ context.Context, command string) (string, error) {
		if command != "passphrase-command" {
			t.Error("wrong passphrase helper")
		}
		return "passphrase", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	_ = tunnel.Close()
	if _, err := OpenMachine(context.Background(), encryptedCfg, MachineOptions{ExecCredential: func(context.Context, string) (string, error) { return "wrong", nil }}); err == nil {
		t.Fatal("wrong key passphrase accepted")
	}
	encryptedCfg.PassphraseCommand = ""
	if _, err := OpenMachine(context.Background(), encryptedCfg, MachineOptions{}); err == nil {
		t.Fatal("encrypted key without configured helper did not fail")
	}
	agentSocket := reviewStartAgent(t, pem, false)
	agentCfg := cfg
	agentCfg.IdentityFile = ""
	agentCfg.IdentityFromAgent = true
	tunnel, err = OpenMachine(context.Background(), agentCfg, MachineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_ = tunnel.Close()
	reviewWaitAgent(t, agentSocket.closed, "successful machine agent release")
}

func TestMachineSSH_Auth_HelperCancellationAndBounds(t *testing.T) {
	host, _ := genKey(t)
	identity, pem := genKey(t)
	srv := startServer(t, host, identity.PublicKey())
	cfg := machineSSHProfile(t, srv, pem)
	for _, timeout := range []time.Duration{-time.Second, 11 * time.Second} {
		if _, err := OpenMachine(context.Background(), cfg, MachineOptions{ConnectTimeout: timeout}); err == nil {
			t.Fatal("invalid connect timeout accepted")
		}
	}
	cfg.IdentityFile = ""
	cfg.SSHPasswordCommand = "trusted"
	for _, cancelled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		entered := make(chan struct{})
		result := make(chan error, 1)
		go func() {
			_, err := OpenMachine(ctx, cfg, MachineOptions{ConnectTimeout: 30 * time.Millisecond, ExecCredential: func(ctx context.Context, _ string) (string, error) {
				close(entered)
				<-ctx.Done()
				return "", ctx.Err()
			}})
			result <- err
		}()
		<-entered
		if cancelled {
			cancel()
			cancel()
		}
		select {
		case err := <-result:
			var safe *MachineError
			if !errors.As(err, &safe) || safe.Code != "TIMEOUT" {
				t.Fatalf("helper timeout/cancel: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("helper did not join")
		}
		cancel()
	}
	cfg.SSHPasswordCommand = ""
	cfg.IdentityFromAgent = true
	t.Setenv("SSH_AUTH_SOCK", "")
	if _, err := OpenMachine(context.Background(), cfg, MachineOptions{}); err == nil {
		t.Fatal("missing agent accepted")
	}
	t.Setenv("SSH_AUTH_SOCK", "synthetic")
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); _ = agent.ServeAgent(agent.NewKeyring(), server) }()
	_, err := OpenMachine(context.Background(), cfg, MachineOptions{DialAgent: func(context.Context, string, string) (net.Conn, error) { return client, nil }})
	_ = server.Close()
	reviewWaitAgent(t, done, "empty agent close")
	if err == nil {
		t.Fatal("empty agent accepted")
	}
}

func TestMachineSSH_AgentCancellation(t *testing.T) {
	host, _ := genKey(t)
	identity, pem := genKey(t)
	srv := startServer(t, host, identity.PublicKey())
	t.Run("connect", func(t *testing.T) {
		cfg := machineSSHProfile(t, srv, pem)
		cfg.IdentityFile = ""
		cfg.IdentityFromAgent = true
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		_, err := OpenMachine(ctx, cfg, MachineOptions{DialAgent: func(ctx context.Context, _, _ string) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }})
		var safe *MachineError
		if !errors.As(err, &safe) || safe.Code != "TIMEOUT" {
			t.Fatalf("agent connect timeout: %v", err)
		}
	})
	t.Run("list", func(t *testing.T) {
		cfg := machineSSHProfile(t, srv, pem)
		cfg.IdentityFile = ""
		cfg.IdentityFromAgent = true
		socket := reviewStartAgent(t, pem, true)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		result := make(chan error, 1)
		go func() { _, err := OpenMachine(ctx, cfg, MachineOptions{}); result <- err }()
		reviewWaitAgent(t, socket.requested, "agent list request")
		cancel()
		select {
		case err := <-result:
			var safe *MachineError
			if !errors.As(err, &safe) || safe.Code != "TIMEOUT" {
				t.Fatalf("agent list cancellation: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("agent list cancellation exceeded2s")
		}
		reviewWaitAgent(t, socket.closed, "cancelled agent list close")
	})
	t.Run("sign", func(t *testing.T) {
		cfg := machineSSHProfile(t, srv, pem)
		cfg.IdentityFile = ""
		cfg.IdentityFromAgent = true
		key, err := ssh.ParseRawPrivateKey(pem)
		if err != nil {
			t.Fatal(err)
		}
		keyring := agent.NewKeyring()
		if err := keyring.Add(agent.AddedKey{PrivateKey: key}); err != nil {
			t.Fatal(err)
		}
		blocking := &machineBlockingAgent{Agent: keyring, entered: make(chan struct{}), release: make(chan struct{})}
		client, server := net.Pipe()
		done := make(chan struct{})
		go func() { defer close(done); _ = agent.ServeAgent(blocking, server) }()
		t.Cleanup(func() {
			close(blocking.release)
			_ = client.Close()
			_ = server.Close()
			reviewWaitAgent(t, done, "blocked signer fixture join")
		})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		result := make(chan error, 1)
		go func() {
			_, err := OpenMachine(ctx, cfg, MachineOptions{DialAgent: func(context.Context, string, string) (net.Conn, error) { return client, nil }})
			result <- err
		}()
		reviewWaitAgent(t, blocking.entered, "agent sign request")
		cancel()
		select {
		case err := <-result:
			var safe *MachineError
			if !errors.As(err, &safe) || safe.Code != "TIMEOUT" {
				t.Fatalf("agent sign cancellation: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("agent signer cancellation exceeded2s")
		}
	})
}

type machineBlockingAgent struct {
	agent.Agent
	entered, release chan struct{}
	once             sync.Once
}

func TestMachineSSH_Auth_AgentThenIdentity(t *testing.T) {
	host, _ := genKey(t)
	identity, pem := genKey(t)
	_, unauthorizedPEM := genKey(t)
	srv := startServer(t, host, identity.PublicKey())
	cfg := machineSSHProfile(t, srv, pem)
	socket := reviewStartAgent(t, unauthorizedPEM, false)
	cfg.IdentityFromAgent = true
	tunnel, err := OpenMachine(context.Background(), cfg, MachineOptions{})
	if err != nil {
		t.Fatalf("valid identity after unauthorized agent: %v", err)
	}
	_ = tunnel.Close()
	reviewWaitAgent(t, socket.closed, "mixed-auth agent release")
}

func (a *machineBlockingAgent) Sign(ssh.PublicKey, []byte) (*ssh.Signature, error) {
	a.once.Do(func() { close(a.entered) })
	<-a.release
	return nil, errors.New("fixture signer released")
}

func TestMachineSSH_Auth_StrictTrustBeforeHelpers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing")
	cfg := models.SSHTunnelConfig{Host: "127.0.0.1", Port: 22, User: "user", KnownHosts: path, SSHPasswordCommand: "never-run"}
	called := false
	_, err := OpenMachine(context.Background(), cfg, MachineOptions{ExecCredential: func(context.Context, string) (string, error) { called = true; return "secret", nil }})
	if err == nil || called {
		t.Fatal("missing trust executed helper")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing trust file created")
	}
}
