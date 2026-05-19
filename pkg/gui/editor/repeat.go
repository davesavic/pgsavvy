package editor

// RepeatStore tracks the last-completed motion / operator / text-object
// state for the vim `.` repeat and the in-flight operator-pending stash
// (PendingOpID). wwd.1 ships this empty shell so QueryEditorContext can
// hold a *RepeatStore pointer; wwd.9 fills in the fields:
//
//	LastOpID, LastMotionID, LastTextObjectID rune-ID stash
//	LastCount, LastRegister                  the `3"ad2w` decoration
//	PendingOpID                              op-pending state-machine slot
//
// Keeping the body empty in wwd.1 avoids speculative fields that wwd.9
// would have to rename when the action-ID conventions land.
type RepeatStore struct{}
