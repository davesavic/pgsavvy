package models

import "time"

// SessionID uniquely identifies a Session within a single process. See
// DESIGN.md §11.1 (Session.ID()).
type SessionID uint64

// QueryID identifies an in-flight query for cancellation and result routing.
// BackendPID is sized to match pgx's pgconn.PgConn.PID() (uint32); this
// supersedes the int32 shown in DESIGN.md §12.4.
type QueryID struct {
	SessionID  SessionID
	BackendPID uint32
	Started    time.Time
	Nonce      uint64
}
