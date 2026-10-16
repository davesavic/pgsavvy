package sshtunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

func machineAgentEndpoint() (string, string, error) {
	address := os.Getenv("SSH_AUTH_SOCK")
	if address == "" {
		address = `\\.\pipe\openssh-ssh-agent`
	}
	network, _, err := machineAgentTarget(address)
	if err != nil {
		return "", "", err
	}
	return network, address, nil
}

func machineAgentTarget(address string) (network, target string, err error) {
	if address == "" || strings.IndexByte(address, 0) >= 0 {
		return "", "", errors.New("agent unavailable")
	}
	path := strings.ReplaceAll(address, "/", `\`)
	for _, prefix := range []string{`\\.\pipe\`, `\\?\pipe\`, `\??\pipe\`} {
		if len(path) < len(prefix) || !strings.EqualFold(path[:len(prefix)], prefix) {
			continue
		}
		tail := path[len(prefix):]
		for _, part := range strings.Split(tail, `\`) {
			if part == "" || part == "." || part == ".." {
				return "", "", errors.New("agent unavailable")
			}
		}
		return "npipe", `\\.\pipe\` + tail, nil
	}
	// No remote UNC/SMB open: it cannot provide the local setup cancellation contract.
	if strings.HasPrefix(path, `\\?\`) || strings.HasPrefix(path, `\??\`) {
		path = path[4:]
		if len(path) < 3 || path[1] != ':' || path[2] != '\\' {
			return "", "", errors.New("agent unavailable")
		}
	} else if strings.HasPrefix(path, `\\`) {
		return "", "", errors.New("agent unavailable")
	}
	return "unix", address, nil
}

func dialMachineAgent(ctx context.Context, network, address string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	kind, target, err := machineAgentTarget(address)
	if err != nil || kind != network {
		return nil, errors.New("agent unavailable")
	}
	if network == "unix" {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, target)
		if err != nil {
			return nil, machineAgentError(err)
		}
		if err := ctx.Err(); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return &machineAgentConn{stream: conn, network: network}, nil
	}
	// The machine contract forbids automatic retries, including busy endpoints.
	file, err := os.OpenFile(target, os.O_RDWR|windows.O_FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, machineAgentError(err)
	}
	if err := ctx.Err(); err != nil {
		_ = file.Close()
		return nil, err
	}
	info, statErr := file.Stat()
	if statErr != nil || info.Mode()&os.ModeNamedPipe == 0 || file.SetDeadline(time.Time{}) != nil {
		_ = file.Close()
		return nil, errors.New("agent unavailable")
	}
	if err := ctx.Err(); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &machineAgentConn{stream: file, network: network}, nil
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
