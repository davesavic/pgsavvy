package context

// ConnectingState is the transient connecting/error state rendered while a
// connection attempt is in flight. It is the tiny shared sink the in-modal
// connect lifecycle writes: connectInvoker drives
// SetConnectingStaged / MarkStage* / SetError on it, and
// ConnectionManagerContext reads BodyGlyph(glyph) in
// ModeConnecting. This keeps connectInvoker's gen/supersession/cancel/worker
// logic unchanged while letting the modal render the connecting body itself
// (instead of pushing the standalone CONNECTING screen).
//
// NOT goroutine-safe — the setters are plain setters the caller serialises
// onto the UI thread, mirroring ConnectingContext.
type ConnectingState struct {
	name   string
	errMsg string
	stages []Stage
}

// StageID identifies a connect lifecycle stage. Stable across a single
// connect attempt; SetConnectingStaged replaces the stage list on retry.
type StageID int

const (
	// StageTunnel is the SSH/tunnel establishment stage.
	StageTunnel StageID = iota
	// StageAuth is the authentication / credential stage.
	StageAuth
	// StageObjects is the schema/object loading stage.
	StageObjects
)

// StageStatus is the lifecycle status of a single stage.
type StageStatus int

const (
	// StagePending is the not-yet-started status.
	StagePending StageStatus = iota
	// StageActive is the in-progress status.
	StageActive
	// StageDone is the successfully-completed status.
	StageDone
	// StageFailed is the failed status.
	StageFailed
)

// Stage is one checklist row in the staged connecting view. Label is the
// caller-supplied human text (T3 supplies real labels); Detail is optional
// trailing text appended after the label (e.g. a count summary).
type Stage struct {
	ID     StageID
	Label  string
	Status StageStatus
	Detail string
}

// SetConnectingStaged puts the state into the connecting phase for the named
// profile, clears any previous error, and REPLACES the stage list so a retry
// starts fresh (prior Failed/Done stages are discarded).
func (s *ConnectingState) SetConnectingStaged(name string, stages []Stage) {
	s.name = name
	s.errMsg = ""
	s.stages = stages
}

// MarkStageActive flips the stage matching id to Active. Idempotent on repeat
// (already-Active is a no-op); an unknown id is a no-op (no panic).
func (s *ConnectingState) MarkStageActive(id StageID) {
	stage := s.stageByID(id)
	if stage == nil {
		return
	}
	stage.Status = StageActive
}

// MarkStageDone flips the stage matching id to Done and sets its Detail.
// Idempotent on repeat; an unknown id is a no-op (no panic).
func (s *ConnectingState) MarkStageDone(id StageID, detail string) {
	stage := s.stageByID(id)
	if stage == nil {
		return
	}
	stage.Status = StageDone
	stage.Detail = detail
}

// MarkStageFailed flips the stage matching id to Failed WITHOUT setting errMsg.
// Distinct from SetError (which flips the active stage AND enters the error
// phase): the schema-load-failure path needs the Objects row to render "✗"
// while the connection itself still succeeds with an empty rail (T3 AD7), so
// the modal must NOT enter the error/retry phase. Idempotent on repeat; an
// unknown id is a no-op (no panic). Mirrors MarkStageActive.
func (s *ConnectingState) MarkStageFailed(id StageID) {
	stage := s.stageByID(id)
	if stage == nil {
		return
	}
	stage.Status = StageFailed
}

// SetStageLabel replaces the display label of the stage matching id. T3 uses it
// to relabel the Objects stage from its in-flight text ("Loading objects…") to
// its terminal text ("Loaded") so the done line reads "✓ Loaded N schemas"
// (N supplied as Detail via MarkStageDone). The constant Tunnel/Auth labels
// never need this. Idempotent; an unknown id is a no-op (no panic).
func (s *ConnectingState) SetStageLabel(id StageID, label string) {
	stage := s.stageByID(id)
	if stage == nil {
		return
	}
	stage.Label = label
}

// stageByID returns a pointer to the stage with the given id, or nil.
func (s *ConnectingState) stageByID(id StageID) *Stage {
	for i := range s.stages {
		if s.stages[i].ID == id {
			return &s.stages[i]
		}
	}
	return nil
}

// SetError puts the state into the error phase with the supplied message and
// marks the currently-Active stage Failed. With no Active stage it only sets
// errMsg (no panic).
func (s *ConnectingState) SetError(msg string) {
	s.errMsg = msg
	for i := range s.stages {
		if s.stages[i].Status == StageActive {
			s.stages[i].Status = StageFailed
		}
	}
}

// IsError reports whether the state is in the error phase (an error message is
// set). False during the active-dial phase, where only Esc (cancel) is allowed
// and retry must not fire — retrying mid-dial supersedes the in-flight attempt
// and re-prompts for credentials.
func (s *ConnectingState) IsError() bool { return s.errMsg != "" }

// BodyGlyph renders the staged checklist (one line per stage) followed by the
// connecting line or the error body. activeGlyph is the spinner glyph drawn
// for the Active stage. With an empty stage list the output is byte-identical
// to the legacy Body() (no leading checklist lines).
func (s *ConnectingState) BodyGlyph(activeGlyph rune) string {
	checklist := ""
	for i := range s.stages {
		checklist += s.stageLine(&s.stages[i], activeGlyph) + "\n"
	}
	if s.errMsg != "" {
		return checklist + s.errMsg + "\n\n[r] retry  [Esc] back"
	}
	return checklist + "Connecting to " + s.name + "…"
}

// stageLine renders a single checklist row: status glyph, label, and optional
// detail appended after a space.
func (s *ConnectingState) stageLine(stage *Stage, activeGlyph rune) string {
	line := string(stageGlyph(stage.Status, activeGlyph)) + " " + stage.Label
	if stage.Detail != "" {
		line += " " + stage.Detail
	}
	return line
}

// stageGlyph maps a stage status to its rendered glyph. Pending uses a dim
// middle-dot marker; Active uses the caller-supplied spinner glyph.
func stageGlyph(status StageStatus, activeGlyph rune) rune {
	switch status {
	case StageDone:
		return '✓'
	case StageFailed:
		return '✗'
	case StageActive:
		return activeGlyph
	default:
		return '·'
	}
}
