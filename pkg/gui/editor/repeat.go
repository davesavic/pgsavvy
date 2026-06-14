package editor

// RepeatStore tracks the last-completed motion / operator / text-object
// state for the vim `.` repeat and the in-flight operator-pending stash
// (PendingOpID). The fields required for op-pending dispatch:
//
//	LastOpID, LastMotionID, LastTextObjectID  // rune-ID stash for `.`
//	LastCount, LastRegister                   // the `3"ad2w` decoration
//	PendingOpID                                // op-pending state-machine slot
//
// The PendingOpID write path is owned by the operator handler (it stashes;
// the motion or text-object handler reads via c.qec.Repeat().PendingOpID
// inside applyPending). The Last* fields are populated when an
// operator+motion completes so the `.` action can replay the last operation.
type RepeatStore struct {
	LastOpID         string
	LastMotionID     string
	LastTextObjectID string
	LastCount        int
	LastRegister     rune
	PendingOpID      string
}

// Capture records a completed operator dispatch so the `.` action
// can replay it. The (motionID, textObjectID) pair is mutually exclusive
// — at most one is non-empty depending on which handler completed the
// op-pending state machine. Empty opID is rejected (defensive: callers
// should never Capture without a real operator to repeat).
func (r *RepeatStore) Capture(opID, motionID, textObjectID string, count int, reg rune) {
	if r == nil || opID == "" {
		return
	}
	r.LastOpID = opID
	r.LastMotionID = motionID
	r.LastTextObjectID = textObjectID
	r.LastCount = count
	r.LastRegister = reg
}

// Replay returns the most-recently-captured operator + count + register
// triple. ok=false when no operator has been captured yet (LastOpID
// empty), so the `.` handler can no-op silently.
func (r *RepeatStore) Replay() (opID string, count int, reg rune, ok bool) {
	if r == nil || r.LastOpID == "" {
		return "", 0, 0, false
	}
	return r.LastOpID, r.LastCount, r.LastRegister, true
}
