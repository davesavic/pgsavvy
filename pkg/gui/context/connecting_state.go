package context

// ConnectingState is the transient connecting/error state rendered while a
// connection attempt is in flight. It is the tiny shared sink the in-modal
// connect lifecycle writes (dbsavvy-1rf): connectInvoker drives SetConnecting
// / SetError on it, and ConnectionManagerContext reads Body() in
// ModeConnecting. This keeps connectInvoker's gen/supersession/cancel/worker
// logic unchanged while letting the modal render the connecting body itself
// (instead of pushing the standalone CONNECTING screen).
//
// NOT goroutine-safe — the setters are plain setters the caller serialises
// onto the UI thread, mirroring ConnectingContext.
type ConnectingState struct {
	name   string
	errMsg string
}

// SetConnecting puts the state into the connecting phase for the named
// profile, clearing any previous error so a retry re-enters the connecting
// phase.
func (s *ConnectingState) SetConnecting(name string) {
	s.name = name
	s.errMsg = ""
}

// SetError puts the state into the error phase with the supplied message.
func (s *ConnectingState) SetError(msg string) {
	s.errMsg = msg
}

// Body returns the rendered text for the current phase: the error body
// (message + retry/back hints) when an error is set, otherwise the
// connecting body. Mirrors ConnectingContext.body so both screens read
// identically.
func (s *ConnectingState) Body() string {
	if s.errMsg != "" {
		return s.errMsg + "\n\n[r] retry  [Esc] back"
	}
	return "Connecting to " + s.name + "…"
}
