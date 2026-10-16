//go:build linux

package sshtunnel

import (
	"context"
	"net"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
)

type MachineError struct{ Code string }

func (e *MachineError) Error() string { return e.Code }

type MachineOptions struct {
	// Injected callbacks must honor context cancellation and return only after
	// their owned work ends. ExecCredential must also bound retained stdout.
	ExecCredential func(context.Context, string) (string, error)
	Dial           func(context.Context, string, string) (net.Conn, error)
	DialAgent      func(context.Context, string, string) (net.Conn, error)
	ConnectTimeout time.Duration
}

// MachineTunnel owns the raw transport; closing only an SSH channel is not
// sufficient to interrupt reads on channels that cannot install deadlines.
type MachineTunnel struct {
	client       *ssh.Client
	raw          net.Conn
	abortOnce    sync.Once
	closeOnce    sync.Once
	stop, joined chan struct{}
}

func machineError(ctx context.Context, code string) error {
	if ctx.Err() != nil {
		return &MachineError{Code: "TIMEOUT"}
	}
	return &MachineError{Code: code}
}

func watchMachineContext(ctx context.Context, abort func()) func() {
	stop, joined := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-ctx.Done():
			abort()
		case <-stop:
		}
	}()
	return sync.OnceFunc(func() { close(stop); <-joined })
}

// OpenMachine requires pre-existing host trust and never prompts or logs.
func OpenMachine(ctx context.Context, cfg models.SSHTunnelConfig, options MachineOptions) (*MachineTunnel, error) {
	if session.ValidateSSHTunnel(&cfg) != nil {
		return nil, machineError(ctx, "CONNECTION_UNAVAILABLE")
	}
	if options.ConnectTimeout == 0 {
		options.ConnectTimeout = 10 * time.Second
	}
	if options.ConnectTimeout <= 0 || options.ConnectTimeout > 10*time.Second {
		return nil, machineError(ctx, "CONNECTION_UNAVAILABLE")
	}
	connectCtx, cancel := context.WithTimeout(ctx, options.ConnectTimeout)
	defer cancel()
	path, err := resolveKnownHostsPath(cfg.KnownHosts)
	if err != nil {
		return nil, machineError(connectCtx, "TRUST_REQUIRED")
	}
	verify, err := knownhosts.New(path)
	if err != nil {
		return nil, machineError(connectCtx, "TRUST_REQUIRED")
	}
	methods, release, err := machineAuth(connectCtx, cfg, options)
	if release != nil {
		defer release()
	}
	if err != nil {
		return nil, err
	}
	dial := options.Dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(session.SSHTunnelPort(&cfg)))
	raw, err := dial(connectCtx, "tcp", addr)
	if err != nil {
		return nil, machineError(connectCtx, "CONNECTION_UNAVAILABLE")
	}
	stop := watchMachineContext(connectCtx, func() { _ = raw.Close() })
	trustFailed := false
	clientCfg := &ssh.ClientConfig{User: cfg.User, Auth: methods, HostKeyCallback: func(host string, remote net.Addr, key ssh.PublicKey) error {
		err := verify(host, remote, key)
		trustFailed = err != nil
		return err
	}}
	conn, chans, requests, err := ssh.NewClientConn(raw, addr, clientCfg)
	stop()
	if err != nil || connectCtx.Err() != nil {
		_ = raw.Close()
		code := "CONNECTION_UNAVAILABLE"
		if trustFailed {
			code = "TRUST_REQUIRED"
		}
		return nil, machineError(connectCtx, code)
	}
	tunnel := &MachineTunnel{client: ssh.NewClient(conn, chans, requests), raw: raw, stop: make(chan struct{}), joined: make(chan struct{})}
	go func() {
		defer close(tunnel.joined)
		select {
		case <-ctx.Done():
			tunnel.abort()
		case <-tunnel.stop:
		}
	}()
	return tunnel, nil
}

func (t *MachineTunnel) abort() { t.abortOnce.Do(func() { _ = t.raw.Close(); _ = t.client.Close() }) }

func (t *MachineTunnel) Close() error {
	t.closeOnce.Do(func() { t.abort(); close(t.stop); <-t.joined })
	return nil
}

func (t *MachineTunnel) Abort() { _ = t.Close() }

func (t *MachineTunnel) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if ctx.Err() != nil {
		return nil, machineError(ctx, "CONNECTION_UNAVAILABLE")
	}
	stop := watchMachineContext(ctx, t.abort)
	conn, err := t.client.Dial(network, addr)
	stop()
	if err != nil || ctx.Err() != nil {
		if conn != nil {
			_ = conn.Close()
		}
		return nil, machineError(ctx, "CONNECTION_UNAVAILABLE")
	}
	return conn, nil
}

// LookupHost preserves the original name for bastion-side DNS and TLS checks.
func (t *MachineTunnel) LookupHost(ctx context.Context, host string) ([]string, error) {
	if ctx.Err() != nil {
		return nil, machineError(ctx, "CONNECTION_UNAVAILABLE")
	}
	return []string{host}, nil
}
