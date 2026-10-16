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
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/sys/windows"
)

type machinePipeFixture struct {
	address string
	handle  windows.Handle
	file    *os.File
}

var machinePipeSequence atomic.Uint64

func machineNewPipe(t *testing.T, access uint32) *machinePipeFixture {
	t.Helper()
	name := fmt.Sprintf(`\\.\pipe\pgsavvy-agent-%d-%d`, os.Getpid(), machinePipeSequence.Add(1))
	wide, err := windows.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateNamedPipe(wide, access|windows.FILE_FLAG_OVERLAPPED|windows.FILE_FLAG_FIRST_PIPE_INSTANCE,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS, 1, 4096, 4096, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := &machinePipeFixture{address: name, handle: handle}
	t.Cleanup(func() {
		if f.file == nil {
			_ = windows.CloseHandle(handle)
		} else {
			_ = f.file.Close()
		}
	})
	return f
}

func machineAgentPair(t *testing.T, network string) (net.Conn, machineAgentStream, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if network == "npipe" {
		f := machineNewPipe(t, windows.PIPE_ACCESS_DUPLEX)
		client, err := dialMachineAgent(ctx, network, f.address)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		// Go's one-shot pipe fixtures connect the client before wrapping the server;
		// no pending native accept or OVERLAPPED/event lifetime is introduced.
		f.file = os.NewFile(uintptr(f.handle), "synthetic-agent-server")
		if f.file == nil || f.file.SetDeadline(time.Time{}) != nil {
			t.Fatal("owned pipe poller initialization failed")
		}
		raw, err := f.file.SyscallConn()
		if err != nil {
			t.Fatal(err)
		}
		var probeErr error
		var kind uint32
		if err := raw.Control(func(fd uintptr) { kind, probeErr = windows.GetFileType(windows.Handle(fd)) }); err != nil || probeErr != nil || kind != windows.FILE_TYPE_PIPE {
			t.Fatal("fixture did not establish a native pipe")
		}
		return client, f.file, f.address
	}
	dir, err := os.MkdirTemp("", "pga-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(dir); err != nil {
			t.Error("owned agent directory cleanup failed")
		}
	})
	address := filepath.Join(dir, "agent")
	listener, err := net.Listen("unix", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	client, err := dialMachineAgent(ctx, network, address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	peer, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	return client, peer, address
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
	if network, address, err := machineAgentEndpoint(); err != nil || network != "npipe" || address != `\\.\pipe\openssh-ssh-agent` {
		t.Fatal("Windows local OpenSSH default changed")
	}
	for _, address := range []string{`\\.\pipe\SECRET-missing`, `//./pipe/SECRET-missing`, filepath.Join(t.TempDir(), "SECRET.sock")} {
		t.Setenv("SSH_AUTH_SOCK", address)
		network, selected, err := machineAgentEndpoint()
		if err != nil || selected != address {
			t.Fatal("explicit selection or injected address shape changed")
		}
		if _, err := dialMachineAgent(context.Background(), network, selected); err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatal("explicit missing endpoint fell back or leaked")
		}
	}
	for _, address := range []string{`\\remote\pipe\SECRET`, `\\?\UNC\remote\pipe\SECRET`, `\??\UNC\remote\pipe\SECRET`, `\\.\GLOBALROOT\Device\NamedPipe\SECRET`, `\\.\pipe\..\SECRET`, `\\.\pipe\`, "\x00SECRET"} {
		t.Setenv("SSH_AUTH_SOCK", address)
		if _, _, err := machineAgentEndpoint(); err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatal("remote/device/invalid endpoint accepted or echoed")
		}
		if _, err := dialMachineAgent(context.Background(), "npipe", address); err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatal("dial bypassed endpoint restrictions")
		}
	}
	denied := machineNewPipe(t, windows.PIPE_ACCESS_INBOUND)
	if conn, err := dialMachineAgent(context.Background(), "npipe", denied.address); err == nil {
		_ = conn.Close()
		t.Fatal("incompatible pipe access unexpectedly succeeded")
	} else if strings.Contains(err.Error(), denied.address) {
		t.Fatal("denied endpoint leaked")
	}
}

func TestMachineAgent_Transport(t *testing.T) {
	for _, network := range []string{"npipe", "unix"} {
		t.Run(network, func(t *testing.T) {
			client, peer, address := machineAgentPair(t, network)
			keyring := machineAgentTestKeyring(t)
			joined := make(chan struct{})
			go func() { _ = agent.ServeAgent(keyring, peer); close(joined) }()
			t.Cleanup(func() { _ = client.Close(); _ = peer.Close(); machineWaitAgent(t, joined) })
			if err := client.SetDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			signers, err := agent.NewClient(client).Signers()
			if err != nil || len(signers) != 1 {
				t.Fatal("native agent listing failed")
			}
			data := []byte("synthetic signing request")
			signature, err := signers[0].Sign(rand.Reader, data)
			if err != nil || signers[0].PublicKey().Verify(data, signature) != nil {
				t.Fatal("native agent signature verification failed")
			}
			if strings.Contains(client.RemoteAddr().String(), address) || client.RemoteAddr().Network() != network {
				t.Fatal("transport endpoint disclosed")
			}
		})
	}
}

func TestMachineAgent_DeadlineAndClose(t *testing.T) {
	for i := range 20 {
		t.Run(fmt.Sprintf("pipe-%d", i), func(t *testing.T) {
			client, peer, address := machineAgentPair(t, "npipe")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			start := time.Now()
			if conn, err := dialMachineAgent(ctx, "npipe", address); err == nil || ctx.Err() != nil || time.Since(start) > 2*time.Second {
				if conn != nil {
					_ = conn.Close()
				}
				t.Fatal("busy selected pipe did not fail without retry")
			}
			cancel()
			cancel()
			if _, err := dialMachineAgent(ctx, "npipe", address); !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation-before-submission was not observed")
			}
			if err := client.SetReadDeadline(time.Now().Add(5 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			if _, err := client.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatal("native pipe read deadline failed")
			}
			if err := client.SetDeadline(time.Time{}); err != nil {
				t.Fatal(err)
			}
			machineAgentReadRecovery(t, client, peer)
			joined := make(chan struct{})
			go func() { _, _ = client.Read(make([]byte, 1)); close(joined) }()
			machineAgentCloseJoined(t, client, peer, joined)
			_ = client.Close()
			machineWaitAgent(t, joined)
		})
	}
	for _, recovery := range []bool{true, false} {
		t.Run(fmt.Sprintf("write-recovery-%v", recovery), func(t *testing.T) {
			client, peer, _ := machineAgentPair(t, "npipe")
			machineAgentBackpressure(t, client)
			if err := client.SetDeadline(time.Time{}); err != nil {
				t.Fatal(err)
			}
			if recovery {
				machineAgentWriteRecovery(t, client, peer)
				return
			}
			started, done := make(chan struct{}), make(chan struct{})
			var writeErr error
			go func() { close(started); _, writeErr = client.Write(make([]byte, 8<<20)); close(done) }()
			machineWaitAgent(t, started)
			select {
			case <-done:
				t.Fatal("write did not remain backpressured after clearing its deadline")
			case <-time.After(20 * time.Millisecond):
			}
			machineAgentCloseJoined(t, client, peer, done)
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
			client, peer, _ := machineAgentPair(t, "npipe")
			requested, release := make(chan struct{}), make(chan struct{})
			keyring := machineAgentTestKeyring(t)
			serverDone := make(chan struct{})
			go func() {
				defer close(serverDone)
				if signing {
					_ = agent.ServeAgent(machineBlockedSigningAgent{keyring, requested, release}, peer)
				} else {
					if _, err := io.ReadFull(peer, make([]byte, 1)); err == nil {
						close(requested)
					}
					<-release
				}
			}()
			ctx, cancel := context.WithCancel(context.Background())
			joined, clientDone := make(chan struct{}), make(chan struct{})
			go func() { <-ctx.Done(); _ = client.Close(); close(joined) }()
			go func() {
				defer close(clientDone)
				signers, err := agent.NewClient(client).Signers()
				if signing && err == nil && len(signers) != 0 {
					_, _ = signers[0].Sign(rand.Reader, []byte("synthetic"))
				}
			}()
			t.Cleanup(func() {
				cancel()
				close(release)
				_ = client.Close()
				_ = peer.Close()
				machineWaitAgent(t, joined)
				machineWaitAgent(t, clientDone)
				machineWaitAgent(t, serverDone)
			})
			machineWaitAgent(t, requested)
			other, otherPeer, _ := machineAgentPair(t, "npipe")
			otherKeys := machineAgentTestKeyring(t)
			otherDone := make(chan struct{})
			go func() { _ = agent.ServeAgent(otherKeys, otherPeer); close(otherDone) }()
			t.Cleanup(func() { _ = other.Close(); _ = otherPeer.Close(); machineWaitAgent(t, otherDone) })
			cancel()
			machineWaitAgent(t, clientDone)
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
