package drivers

// ConnectStage identifies a milestone reached while Driver.Open establishes a
// connection. The orchestrator observes these to surface otherwise-opaque
// progress (e.g. an SSH tunnel coming up, authentication succeeding) that is
// hidden inside the driver's Open call.
type ConnectStage int

const (
	// StageTunnel is reported once an SSH tunnel has been established. It is
	// emitted only when the profile carries a tunnel configuration.
	StageTunnel ConnectStage = iota
	// StageAuthenticated is reported once the pool has been pinged
	// successfully — i.e. the server accepted the credentials.
	StageAuthenticated
)

// String renders a ConnectStage for logs and diagnostics.
func (s ConnectStage) String() string {
	switch s {
	case StageTunnel:
		return "tunnel"
	case StageAuthenticated:
		return "authenticated"
	default:
		return "unknown"
	}
}

// ProgressReporter receives ConnectStage milestones as Driver.Open progresses.
//
// A nil ProgressReporter is a valid no-op: Open and ConnectHelper.Connect
// accept nil and simply skip reporting. Because an interface value can hold a
// nil concrete pointer that cannot be called, emit sites guard with an explicit
// `if reporter != nil` rather than relying on the implementation to nil-check.
//
// Report may be invoked from the goroutine that runs Driver.Open, so any
// implementation MUST be safe for concurrent use with the caller's goroutine.
type ProgressReporter interface {
	Report(stage ConnectStage)
}

// ReportStage emits stage to reporter, treating a nil reporter as a no-op. The
// explicit nil guard lives here so emit sites in drivers stay a single call and
// the conditional-emit logic is unit-testable without a live database.
func ReportStage(reporter ProgressReporter, stage ConnectStage) {
	if reporter == nil {
		return
	}
	reporter.Report(stage)
}
