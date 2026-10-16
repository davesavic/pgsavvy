//go:build linux || darwin

package sshtunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"time"
)

func machineAgentEndpoint() (string, string, error) {
	address := os.Getenv("SSH_AUTH_SOCK")
	if address == "" {
		return "", "", errors.New("agent unavailable")
	}
	return "unix", address, nil
}

func dialMachineAgent(ctx context.Context, network, address string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if network != "unix" || address == "" {
		return nil, errors.New("agent unavailable")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
	if err != nil {
		return nil, machineAgentError(err)
	}
	if err := ctx.Err(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &machineAgentConn{stream: conn, network: network}, nil
}

type machineAgentStream interface {
	io.ReadWriteCloser
	SetDeadline(time.Time) error
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

type machineAgentConn struct {
	stream  machineAgentStream
	network string
}

func (c *machineAgentConn) Read(b []byte) (int, error) {
	n, err := c.stream.Read(b)
	return n, machineAgentError(err)
}

func (c *machineAgentConn) Write(b []byte) (int, error) {
	n, err := c.stream.Write(b)
	return n, machineAgentError(err)
}

func (c *machineAgentConn) Close() error { return machineAgentError(c.stream.Close()) }
func (c *machineAgentConn) SetDeadline(t time.Time) error {
	return machineAgentError(c.stream.SetDeadline(t))
}

func (c *machineAgentConn) SetReadDeadline(t time.Time) error {
	return machineAgentError(c.stream.SetReadDeadline(t))
}

func (c *machineAgentConn) SetWriteDeadline(t time.Time) error {
	return machineAgentError(c.stream.SetWriteDeadline(t))
}

func (c *machineAgentConn) LocalAddr() net.Addr  { return machineAgentAddress(c.network) }
func (c *machineAgentConn) RemoteAddr() net.Addr { return machineAgentAddress(c.network) }

type machineAgentAddress string

func (a machineAgentAddress) Network() string { return string(a) }
func (machineAgentAddress) String() string    { return "ssh-agent" }

func machineAgentError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, os.ErrDeadlineExceeded):
		return os.ErrDeadlineExceeded
	case errors.Is(err, net.ErrClosed), errors.Is(err, os.ErrClosed):
		return net.ErrClosed
	case errors.Is(err, io.EOF):
		return io.EOF
	default:
		return errors.New("agent unavailable")
	}
}
