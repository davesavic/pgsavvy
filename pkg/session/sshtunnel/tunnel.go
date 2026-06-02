// Package sshtunnel opens a driver-agnostic SSH connection to a bastion and
// exposes a DialContext suitable for use as a database driver's dial function
// (e.g. pgx's cfg.ConnConfig.DialFunc). Host-key verification is
// trust-on-first-use; auth supports identity files (incl. non-interactive
// encrypted keys via passphrase_command) and ssh-agent.
package sshtunnel

import (
	"context"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/davesavic/dbsavvy/pkg/logs"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/session"
)

// logCat is the telemetry category for sshtunnel events.
const logCat = "sshtunnel"

// globalLogger is the package-level *slog.Logger used by instrumentation.
// Unset → emits no-op (logs.Event tolerates nil). Mirrors the pg driver's
// atomic.Pointer pattern so Open needs no logger argument.
var globalLogger atomic.Pointer[slog.Logger]

// SetGlobalLogger installs the package logger used by instrumentation.
// Safe to call multiple times (last write wins); nil disables emits.
func SetGlobalLogger(l *slog.Logger) { globalLogger.Store(l) }

// pkgLogger returns the installed package logger or nil.
func pkgLogger() *slog.Logger { return globalLogger.Load() }

// handshakeTimeout bounds the SSH client config's per-handshake timeout. The
// outer context still governs the overall dial; this is a backstop.
const handshakeTimeout = 30 * time.Second

// Tunnel wraps an established *ssh.Client. Its DialContext opens channels to
// downstream addresses (the database host) through the bastion. Close is
// idempotent.
type Tunnel struct {
	client    *ssh.Client
	closeOnce sync.Once
	closeErr  error
}

// Open validates cfg, builds auth methods, verifies the host key (TOFU), and
// establishes an SSH client to the bastion. The supplied ctx bounds the TCP
// dial and the SSH handshake; cancelling it unblocks a stalled handshake.
func Open(ctx context.Context, cfg models.SSHTunnelConfig) (*Tunnel, error) {
	if err := session.ValidateSSHTunnel(&cfg); err != nil {
		return nil, err
	}
	port := session.SSHTunnelPort(&cfg)

	methods, selected, err := authMethods(ctx, cfg)
	if err != nil {
		return nil, err
	}

	hostKey, err := hostKeyCallback(cfg.KnownHosts)
	if err != nil {
		return nil, err
	}

	clientConfig := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            methods,
		HostKeyCallback: hostKey,
		Timeout:         handshakeTimeout,
	}

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(port))

	logs.Event(pkgLogger(), logCat, "open_start",
		slog.String("addr", addr),
		slog.Any("auth", selected),
	)

	client, err := dialSSH(ctx, addr, clientConfig)
	if err != nil {
		logs.Event(pkgLogger(), logCat, "open_error", slog.String("addr", addr))
		return nil, err
	}

	logs.Event(pkgLogger(), logCat, "open_ok", slog.String("addr", addr))
	return &Tunnel{client: client}, nil
}

// dialSSH performs a context-aware SSH dial: the raw TCP connection honors ctx
// via net.Dialer.DialContext, and the (context-less) ssh.NewClientConn
// handshake runs in a goroutine so ctx cancellation can abort a stall.
func dialSSH(ctx context.Context, addr string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	rawConn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, dialErr("tcp dial to bastion", err)
	}

	type result struct {
		conn  ssh.Conn
		chans <-chan ssh.NewChannel
		reqs  <-chan *ssh.Request
		err   error
	}
	done := make(chan result, 1)
	go func() {
		c, chans, reqs, err := ssh.NewClientConn(rawConn, addr, cfg)
		done <- result{conn: c, chans: chans, reqs: reqs, err: err}
	}()

	select {
	case <-ctx.Done():
		_ = rawConn.Close()
		return nil, dialErr("handshake cancelled", ctx.Err())
	case r := <-done:
		if r.err != nil {
			_ = rawConn.Close()
			return nil, dialErr("ssh handshake", r.err)
		}
		return ssh.NewClient(r.conn, r.chans, r.reqs), nil
	}
}

// DialContext opens a channel to addr through the tunnel, honoring ctx.
//
// x/crypto v0.36.0 provides (*ssh.Client).DialContext
// (ssh/tcpip.go:343), so we delegate to it directly — no goroutine wrapper
// needed. The returned net.Conn is suitable as pgx's DialFunc output.
func (t *Tunnel) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, err := t.client.DialContext(ctx, network, addr)
	if err != nil {
		return nil, dialErr("dial through tunnel", err)
	}
	return conn, nil
}

// Close releases the underlying ssh.Client. Idempotent: repeated calls return
// the result of the first close and never panic.
func (t *Tunnel) Close() error {
	t.closeOnce.Do(func() {
		logs.Event(pkgLogger(), logCat, "close")
		t.closeErr = t.client.Close()
	})
	return t.closeErr
}
