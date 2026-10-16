//go:build linux

package sshtunnel

import (
	"context"
	"errors"
	"net"
	"os"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/utils"
)

func machineSecret(ctx context.Context, command string, options MachineOptions) (string, error) {
	if options.ExecCredential == nil || command == "" {
		return "", machineError(ctx, "CREDENTIALS_UNAVAILABLE")
	}
	helperCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	secret, err := options.ExecCredential(helperCtx, command)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "", &MachineError{Code: "TIMEOUT"}
	}
	if err != nil || helperCtx.Err() != nil || len(secret) == 0 || len(secret) > 32<<10 {
		return "", machineError(helperCtx, "CREDENTIALS_UNAVAILABLE")
	}
	return secret, nil
}

func machineAuth(ctx context.Context, cfg models.SSHTunnelConfig, options MachineOptions) ([]ssh.AuthMethod, func(), error) {
	methods := []ssh.AuthMethod{}
	var signers []ssh.Signer
	var release func()
	if cfg.IdentityFromAgent {
		sock := os.Getenv("SSH_AUTH_SOCK")
		if sock == "" {
			return nil, nil, machineError(ctx, "CREDENTIALS_UNAVAILABLE")
		}
		dial := options.DialAgent
		if dial == nil {
			dial = (&net.Dialer{}).DialContext
		}
		conn, err := dial(ctx, "unix", sock)
		if err != nil {
			return nil, nil, machineError(ctx, "CREDENTIALS_UNAVAILABLE")
		}
		if deadline, ok := ctx.Deadline(); ok {
			_ = conn.SetDeadline(deadline)
		}
		stop := watchMachineContext(ctx, func() { _ = conn.Close() })
		release = func() { _ = conn.Close(); stop() }
		agentSigners, err := agent.NewClient(conn).Signers()
		if err != nil || len(agentSigners) == 0 {
			return nil, release, machineError(ctx, "CREDENTIALS_UNAVAILABLE")
		}
		signers = append(signers, agentSigners...)
	}
	if cfg.IdentityFile != "" {
		path, err := utils.ExpandHome(cfg.IdentityFile)
		if err != nil {
			return nil, release, machineError(ctx, "CREDENTIALS_UNAVAILABLE")
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return nil, release, machineError(ctx, "CREDENTIALS_UNAVAILABLE")
		}
		pem, err := os.ReadFile(path)
		if err != nil {
			return nil, release, machineError(ctx, "CREDENTIALS_UNAVAILABLE")
		}
		signer, err := ssh.ParsePrivateKey(pem)
		if err != nil {
			var missing *ssh.PassphraseMissingError
			if !errors.As(err, &missing) {
				return nil, release, machineError(ctx, "CREDENTIALS_UNAVAILABLE")
			}
			secret, secretErr := machineSecret(ctx, cfg.PassphraseCommand, options)
			if secretErr != nil {
				return nil, release, secretErr
			}
			signer, err = ssh.ParsePrivateKeyWithPassphrase(pem, []byte(secret))
			if err != nil {
				return nil, release, machineError(ctx, "CREDENTIALS_UNAVAILABLE")
			}
		}
		signers = append(signers, signer)
	}
	// SSH tries only the first method of each RFC authentication type.
	if len(signers) > 0 {
		methods = append(methods, ssh.PublicKeys(signers...))
	}
	if cfg.SSHPasswordCommand != "" {
		secret, err := machineSecret(ctx, cfg.SSHPasswordCommand, options)
		if err != nil {
			return nil, release, err
		}
		methods = append(methods, ssh.Password(secret))
	}
	if len(methods) == 0 {
		return nil, release, machineError(ctx, "CREDENTIALS_UNAVAILABLE")
	}
	return methods, release, nil
}
