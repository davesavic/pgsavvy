package session

import "context"

// ExecPasswordCommand is the exported entry point to the credentials
// password_command runner (env-scrub + stderr-scrub + success-path stderr
// discard). sshtunnel reuses it to resolve an encrypted-key passphrase from
// PassphraseCommand. Do NOT fork a parallel exec helper.
func ExecPasswordCommand(ctx context.Context, cmd string) (string, error) {
	return execPasswordCommand(ctx, cmd)
}
