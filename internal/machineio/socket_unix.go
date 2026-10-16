//go:build linux || darwin

package machineio

import "golang.org/x/sys/unix"

func checkStreamSocket(fd int) error {
	kind, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
	if err != nil || kind != unix.SOCK_STREAM {
		return errUnavailable
	}
	return nil
}

func (f *pollStream) receive(b []byte) (int, error) {
	if f.socket {
		n, _, err := unix.Recvfrom(f.fd, b, unix.MSG_DONTWAIT)
		return n, err
	}
	return unix.Read(f.fd, b)
}

func (f *pollStream) send(b []byte) (int, error) {
	if f.socket {
		// Per-call flags never alter the caller's shared file description or
		// socket options. Success counts bytes accepted by the destination.
		return unix.SendmsgN(f.fd, b, nil, nil, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL)
	}
	return unix.Write(f.fd, b)
}
