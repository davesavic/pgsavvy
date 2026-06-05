package sshtunnel

import (
	"context"
	"net"
	"os"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/session"
	"github.com/davesavic/dbsavvy/pkg/utils"
)

// authMethods builds the ordered list of ssh.AuthMethod values from cfg.
// ssh tries them in order. Returns a *DialError when no usable auth is
// configured, or when a configured method cannot be prepared.
//
// The returned string slice describes the selected methods (e.g. "agent",
// "identity") for telemetry; it never contains key material.
func authMethods(ctx context.Context, cfg models.SSHTunnelConfig, prompter session.SecretPrompter) ([]ssh.AuthMethod, []string, error) {
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
		m, err := identityAuth(ctx, cfg, prompter)
		if err != nil {
			return nil, nil, err
		}
		methods = append(methods, m)
		selected = append(selected, "identity")
	}

	if usePasswordAuth(cfg, len(methods), prompter) {
		m, err := passwordAuth(ctx, cfg, prompter)
		if err != nil {
			return nil, nil, err
		}
		methods = append(methods, m)
		selected = append(selected, "password")
	}

	if len(methods) == 0 {
		return nil, nil, dialErr("no auth method configured; set identity_file, identity_from_agent, or ssh_password_command", nil)
	}

	return methods, selected, nil
}

// usePasswordAuth reports whether a password ssh.AuthMethod should be added.
// It is selected when an ssh_password_command is configured (non-interactive),
// or when no key/agent method is configured and an interactive prompter is
// available (prompt fallback). keyOrAgentCount is the number of key/agent
// methods already selected.
func usePasswordAuth(cfg models.SSHTunnelConfig, keyOrAgentCount int, prompter session.SecretPrompter) bool {
	if cfg.SSHPasswordCommand != "" {
		return true
	}
	return keyOrAgentCount == 0 && prompter != nil
}

// passwordAuth resolves the SSH password — via ssh_password_command when set
// (non-interactive), else via the interactive prompter — and returns an
// ssh.Password auth method. Resolution is eager so it honors ctx and so the
// secret never reaches an ssh callback or telemetry.
func passwordAuth(ctx context.Context, cfg models.SSHTunnelConfig, prompter session.SecretPrompter) (ssh.AuthMethod, error) {
	if cfg.SSHPasswordCommand != "" {
		pw, err := session.ExecPasswordCommand(ctx, cfg.SSHPasswordCommand)
		if err != nil {
			return nil, dialErr("ssh_password_command failed", err)
		}
		return ssh.Password(pw), nil
	}

	pw, err := prompter.PromptSecret(ctx, "SSH Password")
	if err != nil {
		return nil, dialErr("ssh password auth: prompt", err)
	}
	return ssh.Password(pw), nil
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
func identityAuth(ctx context.Context, cfg models.SSHTunnelConfig, prompter session.SecretPrompter) (ssh.AuthMethod, error) {
	path, err := utils.ExpandHome(cfg.IdentityFile)
	if err != nil {
		return nil, dialErr("resolve identity file path", err)
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

	pass, err := identityPassphrase(ctx, cfg, prompter)
	if err != nil {
		return nil, err
	}

	signer, err = ssh.ParsePrivateKeyWithPassphrase(pem, []byte(pass))
	if err != nil {
		return nil, dialErr("decrypt identity key", err)
	}
	return ssh.PublicKeys(signer), nil
}

// identityPassphrase resolves the passphrase for an encrypted identity key.
// passphrase_command takes precedence (non-interactive); otherwise an
// available prompter is used (interactive); otherwise a typed DialError directs
// the operator to configure passphrase_command.
func identityPassphrase(ctx context.Context, cfg models.SSHTunnelConfig, prompter session.SecretPrompter) (string, error) {
	if cfg.PassphraseCommand != "" {
		pass, err := session.ExecPasswordCommand(ctx, cfg.PassphraseCommand)
		if err != nil {
			return "", dialErr("passphrase_command failed", err)
		}
		return pass, nil
	}

	if prompter == nil {
		return "", dialErr("encrypted identity key requires a passphrase; set passphrase_command", nil)
	}

	pass, err := prompter.PromptSecret(ctx, "SSH Key Password")
	if err != nil {
		return "", dialErr("decrypt identity key: passphrase prompt", err)
	}
	return pass, nil
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
