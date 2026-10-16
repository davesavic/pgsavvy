//go:build linux || darwin

package sshtunnel

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

type machineUnixAgentFixture struct {
	peer          net.Conn
	ready, joined chan struct{}
	stop          chan struct{}
}

func machineUnixAgent(t *testing.T, serve func(net.Conn, <-chan struct{})) (string, *machineUnixAgentFixture) {
	t.Helper()
	dir, err := os.MkdirTemp("", "pga-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(dir); err != nil {
			t.Error("owned agent directory cleanup failed")
		}
	})
	path := filepath.Join(dir, "agent")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	f := &machineUnixAgentFixture{ready: make(chan struct{}), joined: make(chan struct{}), stop: make(chan struct{})}
	go func() {
		defer close(f.joined)
		conn, err := listener.Accept()
		f.peer = conn
		close(f.ready)
		if err == nil {
			defer func() { _ = conn.Close() }()
			serve(conn, f.stop)
		}
	}()
	t.Cleanup(func() {
		close(f.stop)
		_ = listener.Close()
		machineWaitAgent(t, f.ready)
		if f.peer != nil {
			_ = f.peer.Close()
		}
		machineWaitAgent(t, f.joined)
	})
	return path, f
}

func machineWaitAgent(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("owned agent work did not join within 2 seconds")
	}
}

func machineAgentReadRecovery(t *testing.T, conn net.Conn, peer machineAgentStream) {
	t.Helper()
	readDone, writeDone := make(chan struct{}), make(chan struct{})
	var readErr, writeErr error
	var value [1]byte
	go func() { _, readErr = io.ReadFull(conn, value[:]); close(readDone) }()
	go func() { _, writeErr = peer.Write([]byte{'x'}); close(writeDone) }()
	t.Cleanup(func() {
		_ = conn.Close()
		_ = peer.Close()
		machineWaitAgent(t, readDone)
		machineWaitAgent(t, writeDone)
	})
	machineWaitAgent(t, readDone)
	machineWaitAgent(t, writeDone)
	if readErr != nil || writeErr != nil || value[0] != 'x' {
		t.Fatal("clearing the deadline did not restore communication")
	}
}

func machineAgentCloseJoined(t *testing.T, conn net.Conn, peer machineAgentStream, ioDone <-chan struct{}) {
	t.Helper()
	start := time.Now()
	var workers sync.WaitGroup
	for range 3 {
		workers.Go(func() { _ = conn.Close() })
	}
	workers.Go(func() { <-ioDone })
	joined := make(chan struct{})
	go func() { workers.Wait(); close(joined) }()
	t.Cleanup(func() { _ = conn.Close(); _ = peer.Close(); machineWaitAgent(t, joined) })
	machineWaitAgent(t, joined)
	if time.Since(start) > 2*time.Second {
		t.Fatal("concurrent close and I/O join exceeded 2 seconds")
	}
}

func machineAgentBackpressure(t *testing.T, conn net.Conn) {
	t.Helper()
	if err := conn.SetWriteDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 64<<10)
	for range 1024 {
		if _, err := conn.Write(chunk); err != nil {
			if !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatal("blocked write did not preserve deadline error")
			}
			return
		}
	}
	t.Fatal("fixture did not establish write backpressure")
}

func machineAgentWriteRecovery(t *testing.T, conn net.Conn, peer machineAgentStream) {
	t.Helper()
	drained, written := make(chan struct{}), make(chan struct{})
	var n int
	var writeErr error
	go func() { _, _ = io.Copy(io.Discard, peer); close(drained) }()
	go func() { n, writeErr = conn.Write([]byte{'x'}); close(written) }()
	t.Cleanup(func() { _ = conn.Close(); _ = peer.Close(); machineWaitAgent(t, written); machineWaitAgent(t, drained) })
	machineWaitAgent(t, written)
	if n != 1 || writeErr != nil {
		t.Fatal("clearing the write deadline did not restore writes")
	}
	_ = conn.Close()
	machineWaitAgent(t, drained)
}

func machineAgentTestKeyring(t *testing.T) agent.Agent {
	t.Helper()
	_, pem := genKey(t)
	key, err := ssh.ParseRawPrivateKey(pem)
	if err != nil {
		t.Fatal(err)
	}
	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: key}); err != nil {
		t.Fatal(err)
	}
	return keyring
}

func TestMachineAgent_EndpointPrecedence(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	if _, _, err := machineAgentEndpoint(); err == nil {
		t.Fatal("Unix endpoint selection invented a default")
	}
	path := filepath.Join(t.TempDir(), "SECRET-agent")
	t.Setenv("SSH_AUTH_SOCK", path)
	network, address, err := machineAgentEndpoint()
	if err != nil || network != "unix" || address != path {
		t.Fatal("explicit endpoint or injected address shape changed")
	}
	if _, err := dialMachineAgent(context.Background(), network, address); err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatal("unavailable endpoint leaked or silently fell back")
	}
	if _, err := dialMachineAgent(context.Background(), "tcp", "SECRET"); err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatal("unsupported network accepted or echoed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := dialMachineAgent(ctx, network, address); !errors.Is(err, context.Canceled) {
		t.Fatal("pre-canceled dial classification changed")
	}
}

func TestMachineAgent_Transport(t *testing.T) {
	keyring := machineAgentTestKeyring(t)
	path, fixture := machineUnixAgent(t, func(conn net.Conn, _ <-chan struct{}) { _ = agent.ServeAgent(keyring, conn) })
	conn, err := dialMachineAgent(context.Background(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	signers, err := agent.NewClient(conn).Signers()
	if err != nil || len(signers) != 1 {
		t.Fatal("real agent listing failed")
	}
	data := []byte("synthetic signing request")
	signature, err := signers[0].Sign(rand.Reader, data)
	if err != nil || signers[0].PublicKey().Verify(data, signature) != nil {
		t.Fatal("real agent signature verification failed")
	}
	if strings.Contains(conn.LocalAddr().String()+conn.RemoteAddr().String(), path) || conn.RemoteAddr().Network() != "unix" {
		t.Fatal("transport address exposed endpoint")
	}
	_ = conn.Close()
	machineWaitAgent(t, fixture.joined)
}

func TestMachineAgent_DeadlineAndClose(t *testing.T) {
	for i := range 20 {
		t.Run(fmt.Sprintf("read-close-%d", i), func(t *testing.T) {
			path, fixture := machineUnixAgent(t, func(_ net.Conn, stop <-chan struct{}) { <-stop })
			conn, err := dialMachineAgent(context.Background(), "unix", path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			machineWaitAgent(t, fixture.ready)
			if err := conn.SetReadDeadline(time.Now().Add(5 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatal("read deadline or timeout classification failed")
			}
			if err := conn.SetDeadline(time.Time{}); err != nil {
				t.Fatal(err)
			}
			machineAgentReadRecovery(t, conn, fixture.peer)
			done := make(chan struct{})
			go func() { _, _ = conn.Read(make([]byte, 1)); close(done) }()
			start := time.Now()
			machineAgentCloseJoined(t, conn, fixture.peer, done)
			_ = conn.Close()
			machineWaitAgent(t, done)
			if time.Since(start) > 2*time.Second {
				t.Fatal("read close did not join promptly")
			}
			if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, net.ErrClosed) || strings.Contains(err.Error(), path) {
				t.Fatal("closed transport classification/privacy changed")
			}
		})
	}
	for _, recovery := range []bool{true, false} {
		t.Run(fmt.Sprintf("write-recovery-%v", recovery), func(t *testing.T) {
			path, fixture := machineUnixAgent(t, func(_ net.Conn, stop <-chan struct{}) { <-stop })
			conn, err := dialMachineAgent(context.Background(), "unix", path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			machineWaitAgent(t, fixture.ready)
			machineAgentBackpressure(t, conn)
			if err := conn.SetDeadline(time.Time{}); err != nil {
				t.Fatal(err)
			}
			if recovery {
				machineAgentWriteRecovery(t, conn, fixture.peer)
				return
			}
			started, done := make(chan struct{}), make(chan struct{})
			var writeErr error
			go func() { close(started); _, writeErr = conn.Write(make([]byte, 8<<20)); close(done) }()
			machineWaitAgent(t, started)
			select {
			case <-done:
				t.Fatal("write did not remain backpressured after clearing its deadline")
			case <-time.After(20 * time.Millisecond):
			}
			machineAgentCloseJoined(t, conn, fixture.peer, done)
			if !errors.Is(writeErr, net.ErrClosed) {
				t.Fatal("closing a backpressured write changed its classification")
			}
		})
	}
}

type machineBlockedSigningAgent struct {
	agent.Agent
	requested chan struct{}
	release   <-chan struct{}
}

func (a machineBlockedSigningAgent) Sign(ssh.PublicKey, []byte) (*ssh.Signature, error) {
	close(a.requested)
	<-a.release
	return nil, errors.New("synthetic agent stopped")
}

func TestMachineAgent_StalledProtocol(t *testing.T) {
	for _, signing := range []bool{false, true} {
		t.Run(fmt.Sprint(signing), func(t *testing.T) {
			t.Parallel()
			requested := make(chan struct{})
			keyring := machineAgentTestKeyring(t)
			path, _ := machineUnixAgent(t, func(conn net.Conn, stop <-chan struct{}) {
				if signing {
					_ = agent.ServeAgent(machineBlockedSigningAgent{keyring, requested, stop}, conn)
				} else {
					if _, err := io.ReadFull(conn, make([]byte, 1)); err == nil {
						close(requested)
					}
					<-stop
				}
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			conn, err := dialMachineAgent(ctx, "unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			joined := make(chan struct{})
			go func() { <-ctx.Done(); _ = conn.Close(); close(joined) }()
			done := make(chan struct{})
			go func() {
				defer close(done)
				signers, err := agent.NewClient(conn).Signers()
				if signing && err == nil && len(signers) != 0 {
					_, _ = signers[0].Sign(rand.Reader, []byte("synthetic"))
				}
			}()
			t.Cleanup(func() {
				cancel()
				_ = conn.Close()
				machineWaitAgent(t, done)
				machineWaitAgent(t, joined)
			})
			machineWaitAgent(t, requested)
			otherKeyring := machineAgentTestKeyring(t)
			otherPath, _ := machineUnixAgent(t, func(peer net.Conn, _ <-chan struct{}) { _ = agent.ServeAgent(otherKeyring, peer) })
			other, err := dialMachineAgent(context.Background(), "unix", otherPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = other.Close() })
			cancel()
			machineWaitAgent(t, done)
			machineWaitAgent(t, joined)
			if err := other.SetDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			signers, err := agent.NewClient(other).Signers()
			if err != nil || len(signers) != 1 {
				t.Fatal("canceling another invocation stopped independent listing")
			}
			data := []byte("independent signing request")
			signature, err := signers[0].Sign(rand.Reader, data)
			if err != nil || signers[0].PublicKey().Verify(data, signature) != nil {
				t.Fatal("canceling another invocation stopped independent signing")
			}
		})
	}
}
