//go:build linux || darwin

package sshtunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/davesavic/pgsavvy/pkg/models"
)

func machineSSHProfile(t *testing.T, srv *testServer, pem []byte) models.SSHTunnelConfig {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("SSH_AUTH_SOCK", filepath.Join(dir, "no-agent"))
	path := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(path, []byte(knownhosts.Line([]string{srv.Addr}, srv.HostKey.PublicKey())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return models.SSHTunnelConfig{Host: hostAddr(t, srv.Addr), Port: portAddr(t, srv.Addr), User: "user", KnownHosts: path, IdentityFile: writeIdentity(t, pem)}
}

func TestMachineSSH_Trust(t *testing.T) {
	host, _ := genKey(t)
	identity, pem := genKey(t)
	other, _ := genKey(t)
	srv := startServer(t, host, identity.PublicKey())
	for _, state := range []string{"known", "missing", "empty", "unknown", "changed", "revoked", "default-missing"} {
		t.Run(state, func(t *testing.T) {
			cfg := machineSSHProfile(t, srv, pem)
			original := []byte(knownhosts.Line([]string{srv.Addr}, host.PublicKey()) + "\n")
			switch state {
			case "missing":
				cfg.KnownHosts = filepath.Join(t.TempDir(), "missing-parent", "known_hosts")
			case "empty":
				original = nil
			case "unknown":
				original = []byte(knownhosts.Line([]string{"unrelated:22"}, host.PublicKey()) + "\n")
			case "changed":
				original = []byte(knownhosts.Line([]string{srv.Addr}, other.PublicKey()) + "\n")
			case "revoked":
				original = []byte("@revoked " + knownhosts.Line([]string{srv.Addr}, host.PublicKey()) + "\n")
			case "default-missing":
				cfg.KnownHosts = ""
			}
			if state != "missing" && state != "default-missing" {
				if err := os.WriteFile(cfg.KnownHosts, original, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			tunnel, err := OpenMachine(ctx, cfg, MachineOptions{})
			if state == "known" {
				if err != nil {
					t.Fatal(err)
				}
				_ = tunnel.Close()
			} else {
				var safe *MachineError
				if !errors.As(err, &safe) || safe.Code != "TRUST_REQUIRED" {
					t.Fatalf("%s trust: %v", state, err)
				}
			}
			path, _ := resolveKnownHostsPath(cfg.KnownHosts)
			after, readErr := os.ReadFile(path)
			if state == "missing" || state == "default-missing" {
				if !errors.Is(readErr, os.ErrNotExist) {
					t.Fatal("strict trust created a file")
				}
			} else if readErr != nil || string(after) != string(original) {
				t.Fatal("strict trust modified file")
			}
		})
	}
}

func machineEcho(t *testing.T, tunnel *MachineTunnel, backend string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := tunnel.DialContext(ctx, "tcp", backend)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	stop := watchMachineContext(ctx, tunnel.abort)
	defer stop()
	if _, err := conn.Write([]byte("echo")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(conn, buffer); err != nil || string(buffer) != "echo" {
		t.Fatalf("echo: %q %v", buffer, err)
	}
}

func TestMachineSSH_OwnedAbort(t *testing.T) {
	host, _ := genKey(t)
	identity, pem := genKey(t)
	srv := startServer(t, host, identity.PublicKey())
	cfg := machineSSHProfile(t, srv, pem)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tunnel, err := OpenMachine(ctx, cfg, MachineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tunnel.Close() }()
	other, err := OpenMachine(context.Background(), cfg, MachineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	machineEcho(t, other, srv.Backend)
	conn, err := tunnel.DialContext(ctx, "tcp", srv.Backend)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err == nil {
		t.Fatal("fixture must exercise deadline-unsupported SSH channel")
	}
	read := make(chan error, 1)
	go func() { _, err := conn.Read(make([]byte, 1)); read <- err }()
	start := time.Now()
	cancel()
	cancel()
	select {
	case err := <-read:
		if err == nil {
			t.Fatal("stalled channel succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("owned raw abort did not release channel")
	}
	tunnel.Abort()
	_ = tunnel.Close()
	if time.Since(start) > 2*time.Second {
		t.Fatal("cleanup exceeded2s")
	}
	machineEcho(t, other, srv.Backend)
	lookup, err := other.LookupHost(context.Background(), "bastion-only.invalid")
	if err != nil || len(lookup) != 1 || lookup[0] != "bastion-only.invalid" {
		t.Fatal("lookup changed TLS/bastion identity")
	}
}

func TestMachineSSH_OwnedAbort_Handshake(t *testing.T) {
	host, _ := genKey(t)
	_, pem := genKey(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	srv := &testServer{Addr: ln.Addr().String(), HostKey: host}
	cfg := machineSSHProfile(t, srv, pem)
	accepted, closed := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(closed)
		conn, err := ln.Accept()
		close(accepted)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = io.Copy(io.Discard, conn)
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := OpenMachine(ctx, cfg, MachineOptions{}); result <- err }()
	reviewWaitAgent(t, accepted, "stalled bastion accept")
	cancel()
	select {
	case err := <-result:
		var safe *MachineError
		if !errors.As(err, &safe) || safe.Code != "TIMEOUT" {
			t.Fatalf("stalled handshake: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handshake cleanup exceeded2s")
	}
	reviewWaitAgent(t, closed, "stalled bastion close")
}

func TestMachineSSH_LegacyTOFU(t *testing.T) {
	host, _ := genKey(t)
	identity, pem := genKey(t)
	srv := startServer(t, host, identity.PublicKey())
	cfg := machineSSHProfile(t, srv, pem)
	cfg.KnownHosts = filepath.Join(t.TempDir(), "legacy", "known_hosts")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	legacy, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	_ = legacy.Close()
	data, err := os.ReadFile(cfg.KnownHosts)
	if err != nil || !strings.Contains(string(data), host.PublicKey().Type()) {
		t.Fatalf("legacy TOFU changed: %v", err)
	}
	machine, err := OpenMachine(ctx, cfg, MachineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_ = machine.Close()
}

func TestMachineSSH_OwnedAbort_ChannelOpenAndWrite(t *testing.T) {
	for _, mode := range []string{"open", "write"} {
		t.Run(mode, func(t *testing.T) {
			host, _ := genKey(t)
			_, pem := genKey(t)
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			ready, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			go func() {
				defer close(done)
				raw, err := ln.Accept()
				if err != nil {
					return
				}
				defer func() { _ = raw.Close() }()
				serverCfg := &ssh.ServerConfig{NoClientAuth: true}
				serverCfg.AddHostKey(host)
				sc, chans, reqs, err := ssh.NewServerConn(raw, serverCfg)
				if err != nil {
					return
				}
				defer func() { _ = sc.Close() }()
				go ssh.DiscardRequests(reqs)
				newChannel, ok := <-chans
				if !ok {
					return
				}
				if mode == "write" {
					ch, reqs, err := newChannel.Accept()
					if err != nil {
						return
					}
					defer func() { _ = ch.Close() }()
					go ssh.DiscardRequests(reqs)
				}
				close(ready)
				<-release
			}()
			t.Cleanup(func() { close(release); _ = ln.Close(); reviewWaitAgent(t, done, "channel fixture join") })
			cfg := machineSSHProfile(t, &testServer{Addr: ln.Addr().String(), HostKey: host}, pem)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			openCtx := ctx
			if mode == "open" {
				openCtx = context.Background()
			}
			tunnel, err := OpenMachine(openCtx, cfg, MachineOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tunnel.Close() }()
			result := make(chan error, 1)
			go func() {
				conn, err := tunnel.DialContext(ctx, "tcp", "bastion-only.invalid:5432")
				if err != nil {
					result <- err
					return
				}
				defer func() { _ = conn.Close() }()
				_, err = conn.Write(make([]byte, 4<<20))
				result <- err
			}()
			reviewWaitAgent(t, ready, "pending channel/window fixture")
			select {
			case err := <-result:
				t.Fatalf("operation was not stalled: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			cancel()
			cancel()
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("stalled operation succeeded after abort")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("stalled channel operation exceeded2s cleanup")
			}
		})
	}
}
