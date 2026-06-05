package sshtunnel

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/davesavic/dbsavvy/pkg/utils"
)

// knownHostsMu serializes the read-check-append critical section of the TOFU
// callback. Single-process safe; multi-process collisions on the same
// known_hosts file are an explicit non-goal.
var knownHostsMu sync.Mutex

// hostKeyCallback builds a TOFU (trust-on-first-use) host-key verifier.
//
// knownhosts.New REJECTS unknown keys and never persists them, so we wrap its
// callback: an unknown host is appended to known_hosts and accepted; a key
// mismatch or a revoked key is rejected hard.
//
// knownHostsPath is cfg.KnownHosts (already ~-expanded) when set, else the
// default ~/.ssh/known_hosts. The file (and parent ~/.ssh dir) is created if
// missing because knownhosts.New errors on a missing file.
func hostKeyCallback(knownHostsPath string) (ssh.HostKeyCallback, error) {
	path, err := resolveKnownHostsPath(knownHostsPath)
	if err != nil {
		return nil, err
	}

	if err := ensureKnownHostsFile(path); err != nil {
		return nil, err
	}

	inner, err := knownhosts.New(path)
	if err != nil {
		return nil, dialErr("load known_hosts", err)
	}

	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		knownHostsMu.Lock()
		defer knownHostsMu.Unlock()

		err := inner(hostname, remote, key)
		if err == nil {
			return nil
		}

		var revoked *knownhosts.RevokedError
		if errors.As(err, &revoked) {
			return dialErr("host key revoked", err)
		}

		var keyErr *knownhosts.KeyError
		if !errors.As(err, &keyErr) {
			return dialErr("host key verification", err)
		}
		if len(keyErr.Want) > 0 {
			return dialErr("host key mismatch", err)
		}

		// Unknown host: trust on first use.
		if appendErr := appendKnownHost(path, hostname, key); appendErr != nil {
			return appendErr
		}
		return nil
	}, nil
}

// resolveKnownHostsPath returns the explicit path (~-expanded) when set, else
// the default ~/.ssh/known_hosts.
func resolveKnownHostsPath(explicit string) (string, error) {
	if explicit != "" {
		path, err := utils.ExpandHome(explicit)
		if err != nil {
			return "", dialErr("resolve known_hosts path", err)
		}
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", dialErr("resolve home directory for known_hosts", err)
	}
	return filepath.Join(home, ".ssh", "known_hosts"), nil
}

// ensureKnownHostsFile creates the known_hosts file and its parent ~/.ssh
// directory when missing (dir 0700, file 0600). A pre-existing file is left
// untouched.
func ensureKnownHostsFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return dialErr("create known_hosts directory", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return dialErr("create known_hosts file", err)
	}
	return closeWrap(f, "create known_hosts file")
}

// appendKnownHost appends a single TOFU line for hostname/key. Atomic-ish:
// open O_APPEND|O_WRONLY (re-creating if missing), write one line, close.
func appendKnownHost(path, hostname string, key ssh.PublicKey) error {
	line := knownhosts.Line([]string{hostname}, key)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return dialErr("open known_hosts for append", err)
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		_ = f.Close()
		return dialErr("append to known_hosts", err)
	}
	return closeWrap(f, "append to known_hosts")
}

// closeWrap closes f, wrapping any error as a DialError with msg.
func closeWrap(f *os.File, msg string) error {
	if err := f.Close(); err != nil {
		return dialErr(msg, err)
	}
	return nil
}
