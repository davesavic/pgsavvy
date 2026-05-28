package drivers

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
)

// ErrNotImplemented is returned by driver methods that are stubbed in the
// current scope but will be filled in by a later epic. It is a fresh
// sentinel — NOT errors.ErrUnsupported, which the Go standard library godoc
// forbids aliasing. See epic dbsavvy-921 D4 and DESIGN.md §11.5.
var ErrNotImplemented = errors.New("driver: operation not yet implemented")

// ErrUnknownDriver is wrapped by Get when the requested driver name has not
// been registered.
var ErrUnknownDriver = errors.New("drivers: unknown driver")

// ErrInvalidQueryID is returned by Connection.Cancel when the caller passes a
// QueryID that cannot identify a live backend (e.g. BackendPID == 0). It is a
// precondition-violation sentinel, distinct from network failures on the cancel
// dial — those propagate as wrapped *QueryError values from the driver layer.
// See epic dbsavvy-66p.4.
var ErrInvalidQueryID = errors.New("driver: invalid query id")

// QueryError is the engine-neutral wrapper drivers map their native error
// type into. The query editor underlines the bad token at Position and
// surfaces Hint as a tooltip. See DESIGN.md §11.5.
type QueryError struct {
	Raw        error
	Code       string
	Severity   string
	Hint       string
	Detail     string
	Where      string
	Schema     string
	Table      string
	Column     string
	Constraint string
	Position   int
}

// Error implements error. A nil-receiver or empty QueryError is reported as
// a generic driver query error; a non-nil Raw is rendered through; structured
// fields (Severity/Code/Hint) are appended when set.
func (e *QueryError) Error() string {
	if e == nil {
		return "<nil>"
	}
	var b strings.Builder
	switch {
	case e.Raw != nil:
		b.WriteString(e.Raw.Error())
	case e.Severity != "" || e.Code != "":
		fmt.Fprintf(&b, "%s %s", strings.TrimSpace(e.Severity), e.Code)
	default:
		b.WriteString("driver: query error")
	}
	if e.Hint != "" {
		fmt.Fprintf(&b, " (hint: %s)", e.Hint)
	}
	return strings.TrimSpace(b.String())
}

// Unwrap exposes Raw so errors.Is/As traverse the structured wrapper.
func (e *QueryError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Raw
}

// IsConnectionDead classifies whether err indicates the underlying
// connection is dead and cannot be reused.
//
// Transport-layer errors (net.OpError, io.EOF, io.ErrUnexpectedEOF) are
// the obvious cases. Three additional classes are recognised:
//
//   - pgx's unexported connLockError with message "conn closed" — returned
//     after pgx has torn down the connection (e.g. post-FATAL).
//   - A FATAL-severity QueryError — in PostgreSQL the server always closes
//     the connection after sending FATAL (e.g. 57P01 admin_shutdown).
//   - A raw pgconn.PgError with FATAL severity — the stream termination
//     path passes unstructured errors; detected by the "FATAL: " prefix
//     that pgconn.PgError.Error() always emits.
//
// Context errors (context.Canceled, context.DeadlineExceeded) are NOT
// connection-dead — the wire is still usable after those.
func IsConnectionDead(err error) bool {
	if err == nil {
		return false
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	msg := err.Error()
	if msg == "conn closed" || msg == "conn uninitialized" {
		return true
	}
	var qe *QueryError
	if errors.As(err, &qe) && strings.EqualFold(qe.Severity, "FATAL") {
		return true
	}
	if strings.HasPrefix(msg, "FATAL: ") {
		return true
	}
	return false
}
