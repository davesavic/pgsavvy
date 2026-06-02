package sshtunnel

import "errors"

// DialError wraps an underlying ssh/net error produced while establishing or
// using an SSH tunnel. Its message always names SSH so callers can distinguish
// a tunnel failure from a downstream Postgres error.
//
// HYGIENE: only the underlying error string is ever embedded. Key material,
// passphrases, and DSNs are NEVER formatted into the message or wrapped error.
type DialError struct {
	// msg is a short, static description of the failing stage (e.g.
	// "ssh-agent", "identity key", "host key"). It must not contain secrets.
	msg string
	// err is the underlying ssh/net error. It may be nil for purely
	// configuration-level failures.
	err error
}

// Error renders "ssh tunnel: <msg>" and appends the wrapped error string when
// present.
func (e *DialError) Error() string {
	if e.err == nil {
		return "ssh tunnel: " + e.msg
	}
	return "ssh tunnel: " + e.msg + ": " + e.err.Error()
}

// Unwrap exposes the underlying ssh/net error for errors.Is / errors.As.
func (e *DialError) Unwrap() error { return e.err }

// dialErr is the internal constructor for DialError.
func dialErr(msg string, err error) *DialError {
	return &DialError{msg: msg, err: err}
}

// IsDialError reports whether err (or anything it wraps) is a *DialError.
func IsDialError(err error) bool {
	var de *DialError
	return errors.As(err, &de)
}
