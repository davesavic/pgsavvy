package session

import "context"

// ctxKey is a private type so context values stored under it cannot collide
// with keys from other packages.
type ctxKey int

const ctxKeyDontLog ctxKey = 1

// WithoutLogging returns a context that suppresses HistoryRecorder.Record
// calls for SQLSession.Execute / Stream invocations carrying it. The
// context-level flag is the only opt-out; callers that want logging on a
// per-call basis omit this wrapper.
func WithoutLogging(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKeyDontLog, true)
}

// LoggingSuppressed reports whether ctx was decorated with WithoutLogging.
// Exported so test helpers and out-of-package executors (e.g. pkg/query)
// can assert the flag's presence without re-implementing the key.
func LoggingSuppressed(ctx context.Context) bool {
	v, _ := ctx.Value(ctxKeyDontLog).(bool)
	return v
}
