package editor

// RepeatStore tracks the last-completed motion / operator / text-object
// state for the vim `.` repeat and the in-flight operator-pending stash
// (PendingOpID). wwd.1 shipped an empty shell; wwd.8 fills in the fields
// required for op-pending dispatch:
//
//	LastOpID, LastMotionID, LastTextObjectID  // rune-ID stash for `.` (wwd.9 consumes)
//	LastCount, LastRegister                   // the `3"ad2w` decoration (wwd.9 consumes)
//	PendingOpID                                // op-pending state-machine slot
//
// wwd.8 owns the PendingOpID write path (operator handler stashes; motion
// or text-object handler reads via c.qec.Repeat().PendingOpID inside
// applyPending). The Last* fields are populated when an operator+motion
// completes so wwd.9's `.` action can replay the last operation. wwd.9
// fills the `.` action handler; wwd.8 only populates the fields.
type RepeatStore struct {
	LastOpID         string
	LastMotionID     string
	LastTextObjectID string
	LastCount        int
	LastRegister     rune
	PendingOpID      string
}

// Capture records a completed operator dispatch so wwd.9's `.` action
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
