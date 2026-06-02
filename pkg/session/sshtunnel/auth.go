package sshtunnel

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/session"
)

// authMethods builds the ordered list of ssh.AuthMethod values from cfg.
// ssh tries them in order. Returns a *DialError when no usable auth is
// configured, or when a configured method cannot be prepared.
//
// The returned string slice describes the selected methods (e.g. "agent",
// "identity") for telemetry; it never contains key material.
func authMethods(ctx context.Context, cfg models.SSHTunnelConfig) ([]ssh.AuthMethod, []string, error) {
	methods := make([]ssh.AuthMethod, 0, 2)
	selected := make([]string, 0, 2)

	if cfg.IdentityFromAgent {
		m, err := agentAuth()
		if err != nil {
			return nil, nil, err
		}
		methods = append(methods, m)
		selected = append(selected, "agent")
	}

	if cfg.IdentityFile != "" {
		m, err := identityAuth(ctx, cfg)
		if err != nil {
			return nil, nil, err
		}
		methods = append(methods, m)
		selected = append(selected, "identity")
	}

	if len(methods) == 0 {
		return nil, nil, dialErr("no auth method configured; set identity_file or identity_from_agent", nil)
	}

	return methods, selected, nil
}

// agentAuth dials SSH_AUTH_SOCK and returns a public-keys-callback auth method
// backed by the running ssh-agent. A missing/empty SSH_AUTH_SOCK yields a
// typed DialError naming ssh-agent rather than a nil deref.
func agentAuth() (ssh.AuthMethod, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, dialErr("ssh-agent requested but SSH_AUTH_SOCK is unset", nil)
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, dialErr("ssh-agent dial failed", err)
	}
	ag := agent.NewClient(conn)
	return ssh.PublicKeysCallback(ag.Signers), nil
}

// identityAuth reads and parses the identity file referenced by cfg. Encrypted
// keys are unlocked via PassphraseCommand when set; otherwise a typed
// DialError directs the operator to configure passphrase_command.
func identityAuth(ctx context.Context, cfg models.SSHTunnelConfig) (ssh.AuthMethod, error) {
	path, err := expandHome(cfg.IdentityFile)
	if err != nil {
		return nil, err
	}

	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, dialErr("read identity file", err)
	}

	signer, err := ssh.ParsePrivateKey(pem)
	if err == nil {
		return ssh.PublicKeys(signer), nil
	}

	var missing *ssh.PassphraseMissingError
	if !asPassphraseMissing(err, &missing) {
		return nil, dialErr("parse identity key", err)
	}

	if cfg.PassphraseCommand == "" {
		return nil, dialErr("encrypted identity key requires a passphrase; set passphrase_command (interactive prompt deferred to T4)", nil)
	}

	pass, err := session.ExecPasswordCommand(ctx, cfg.PassphraseCommand)
	if err != nil {
		return nil, dialErr("passphrase_command failed", err)
	}

	signer, err = ssh.ParsePrivateKeyWithPassphrase(pem, []byte(pass))
	if err != nil {
		return nil, dialErr("decrypt identity key", err)
	}
	return ssh.PublicKeys(signer), nil
}

// asPassphraseMissing reports whether err is a *ssh.PassphraseMissingError,
// binding it into target. Thin wrapper kept local so identityAuth reads as a
// linear set of early returns.
func asPassphraseMissing(err error, target **ssh.PassphraseMissingError) bool {
	pm, ok := err.(*ssh.PassphraseMissingError)
	if ok {
		*target = pm
	}
	return ok
}

// expandHome resolves a leading "~" to the user's home directory. A bare "~"
// or "~/..." is expanded; any other path is returned unchanged. A missing HOME
// (os.UserHomeDir error) yields a typed DialError rather than a panic.
func expandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", dialErr("resolve home directory for ~ expansion", err)
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}
