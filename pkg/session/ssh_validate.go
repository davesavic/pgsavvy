package session

import (
	"errors"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// defaultSSHPort is the port a tunnel falls back to when the profile leaves
// SSHTunnelConfig.Port unset (zero).
const defaultSSHPort = 22

// ErrSSHTunnelHostMissing is returned by ValidateSSHTunnel when a non-nil
// tunnel omits Host. It is the SSH analogue of ErrStatementTimeoutInvalid:
// a typed sentinel callers (T2 dialer) can match with errors.Is.
var ErrSSHTunnelHostMissing = errors.New("session: ssh tunnel requires a host")

// ErrSSHTunnelUserMissing is returned by ValidateSSHTunnel when a non-nil
// tunnel omits User.
var ErrSSHTunnelUserMissing = errors.New("session: ssh tunnel requires a user")

// ValidateSSHTunnel checks a tunnel config for the minimum fields the dialer
// needs. A nil tunnel means "no tunnel" and validates trivially. Host and User
// are mandatory; everything else (auth method, port) is the dialer's concern.
func ValidateSSHTunnel(t *models.SSHTunnelConfig) error {
	if t == nil {
		return nil
	}
	if t.Host == "" {
		return ErrSSHTunnelHostMissing
	}
	if t.User == "" {
		return ErrSSHTunnelUserMissing
	}
	return nil
}

// SSHTunnelPort returns the effective TCP port for a tunnel, canonicalizing a
// zero (unset) Port to defaultSSHPort. It is pure — callers get the port
// without mutating the shared model — so the T2 dialer can resolve the port
// without side effects on a profile that may be reused across connections.
func SSHTunnelPort(t *models.SSHTunnelConfig) int {
	if t == nil || t.Port == 0 {
		return defaultSSHPort
	}
	return t.Port
}
