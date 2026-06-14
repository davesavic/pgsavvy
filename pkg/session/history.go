package session

// HistoryRecorder receives one Record call per terminated run on a SQLSession.
// The concrete implementation that persists to the command log is wired in
// elsewhere; this package only defines the contract and a noop
// fallback. SQLSession wraps Record calls in a recover so a misbehaving
// recorder cannot take down a session.
type HistoryRecorder interface {
	Record(stmt string, durMs int64, rowsAffected int64, succeeded bool)
}

// noopHistoryRecorder is the default when Options.HistoryRecorder is nil.
type noopHistoryRecorder struct{}

func (noopHistoryRecorder) Record(string, int64, int64, bool) {}
