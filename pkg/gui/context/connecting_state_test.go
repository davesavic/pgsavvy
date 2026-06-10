package context

import "testing"

// Test_ConnectingState_EmptyStageListBody asserts that with an empty stage
// list (the migrated zero-stage path) BodyGlyph renders the bare connecting /
// error body with no leading checklist lines — byte-identical to the legacy
// output. T3 removed the zero-arg SetConnecting / Body shims; the empty-stage
// staged path is the replacement and must preserve this rendering.
func Test_ConnectingState_EmptyStageListBody(t *testing.T) {
	tests := []struct {
		name  string
		setup func(s *ConnectingState)
		want  string
	}{
		{
			name:  "connecting phase",
			setup: func(s *ConnectingState) { s.SetConnectingStaged("prod", nil) },
			want:  "Connecting to prod…",
		},
		{
			name: "error phase",
			setup: func(s *ConnectingState) {
				s.SetConnectingStaged("prod", nil)
				s.SetError("dial failed")
			},
			want: "dial failed\n\n[r] retry  [Esc] back",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &ConnectingState{}
			tt.setup(s)
			if got := s.BodyGlyph(' '); got != tt.want {
				t.Errorf("BodyGlyph(' ') = %q; want %q", got, tt.want)
			}
		})
	}
}

// Test_ConnectingState_SetConnectingStagedReplaces asserts SetConnectingStaged
// replaces the stage list and clears a prior error (retry clears prior
// Failed/errMsg).
func Test_ConnectingState_SetConnectingStagedReplaces(t *testing.T) {
	s := &ConnectingState{}
	s.SetConnectingStaged("prod", []Stage{
		{ID: StageAuth, Label: "Authenticate"},
	})
	s.MarkStageActive(StageAuth)
	s.SetError("boom")

	if !s.IsError() {
		t.Fatalf("IsError() = false; want true after SetError")
	}

	// Retry: replace the stage list with a fresh one.
	s.SetConnectingStaged("prod", []Stage{
		{ID: StageAuth, Label: "Authenticate"},
		{ID: StageObjects, Label: "Load objects"},
	})

	if s.IsError() {
		t.Errorf("IsError() = true; want false after retry")
	}
	if len(s.stages) != 2 {
		t.Errorf("len(stages) = %d; want 2 after replace", len(s.stages))
	}
	for _, st := range s.stages {
		if st.Status != StagePending {
			t.Errorf("stage %v Status = %v; want StagePending after replace", st.ID, st.Status)
		}
	}
}

// Test_ConnectingState_BodyGlyphPerStage asserts the per-stage glyph rendering
// (Done✓ / Failed✗ / Active=glyph / Pending=dim) and that Detail is appended
// adjacent to the label.
func Test_ConnectingState_BodyGlyphPerStage(t *testing.T) {
	s := &ConnectingState{}
	s.SetConnectingStaged("prod", []Stage{
		{ID: StageTunnel, Label: "Tunnel", Status: StageDone, Detail: "via bastion"},
		{ID: StageAuth, Label: "Authenticate", Status: StageActive},
		{ID: StageObjects, Label: "Load objects", Status: StagePending},
	})

	want := "✓ Tunnel via bastion\n" +
		"⠙ Authenticate\n" +
		"· Load objects\n" +
		"Connecting to prod…"
	if got := s.BodyGlyph('⠙'); got != want {
		t.Errorf("BodyGlyph =\n%q\nwant\n%q", got, want)
	}
}

// Test_ConnectingState_DetailCountSuffix asserts a count-style Detail renders
// adjacent to the label.
func Test_ConnectingState_DetailCountSuffix(t *testing.T) {
	s := &ConnectingState{}
	s.SetConnectingStaged("prod", []Stage{
		{ID: StageObjects, Label: "Loaded"},
	})
	s.MarkStageDone(StageObjects, "3 schemas")

	want := "✓ Loaded 3 schemas\nConnecting to prod…"
	if got := s.BodyGlyph(' '); got != want {
		t.Errorf("BodyGlyph = %q; want %q", got, want)
	}
}

// Test_ConnectingState_MarkStageMutateByID asserts MarkStageActive/Done mutate
// the stage matching the id, are idempotent on repeat, and that an unknown id
// is a no-op (no panic, no mutation).
func Test_ConnectingState_MarkStageMutateByID(t *testing.T) {
	s := &ConnectingState{}
	s.SetConnectingStaged("prod", []Stage{
		{ID: StageTunnel, Label: "Tunnel"},
		{ID: StageAuth, Label: "Authenticate"},
	})

	s.MarkStageActive(StageAuth)
	if got := s.stageByID(StageAuth).Status; got != StageActive {
		t.Errorf("Auth Status = %v; want StageActive", got)
	}
	if got := s.stageByID(StageTunnel).Status; got != StagePending {
		t.Errorf("Tunnel Status = %v; want StagePending (unaffected)", got)
	}

	// Idempotent repeat.
	s.MarkStageActive(StageAuth)
	if got := s.stageByID(StageAuth).Status; got != StageActive {
		t.Errorf("Auth Status after repeat = %v; want StageActive", got)
	}

	s.MarkStageDone(StageAuth, "ok")
	s.MarkStageDone(StageAuth, "ok")
	if got := s.stageByID(StageAuth); got.Status != StageDone || got.Detail != "ok" {
		t.Errorf("Auth after Done = %+v; want Done/ok", got)
	}

	// Unknown id: no panic, no mutation.
	s.MarkStageActive(StageID(999))
	s.MarkStageDone(StageID(999), "x")
	if len(s.stages) != 2 {
		t.Errorf("len(stages) = %d after unknown-id ops; want 2", len(s.stages))
	}
}

// Test_ConnectingState_SetErrorFlipsActive asserts SetError flips the
// currently-Active stage to Failed; the named "failed auth attributed to the
// auth stage" scenario.
func Test_ConnectingState_SetErrorFlipsActive(t *testing.T) {
	s := &ConnectingState{}
	s.SetConnectingStaged("prod", []Stage{
		{ID: StageTunnel, Label: "Tunnel", Status: StageDone},
		{ID: StageAuth, Label: "Authenticate"},
		{ID: StageObjects, Label: "Load objects"},
	})
	s.MarkStageActive(StageAuth)
	s.SetError("auth rejected")

	if got := s.stageByID(StageAuth).Status; got != StageFailed {
		t.Errorf("Auth Status = %v; want StageFailed (attributed to active stage)", got)
	}
	if got := s.stageByID(StageTunnel).Status; got != StageDone {
		t.Errorf("Tunnel Status = %v; want StageDone (unaffected)", got)
	}
	if got := s.stageByID(StageObjects).Status; got != StagePending {
		t.Errorf("Objects Status = %v; want StagePending (unaffected)", got)
	}

	want := "✓ Tunnel\n" +
		"✗ Authenticate\n" +
		"· Load objects\n" +
		"auth rejected\n\n[r] retry  [Esc] back"
	if got := s.BodyGlyph(' '); got != want {
		t.Errorf("BodyGlyph =\n%q\nwant\n%q", got, want)
	}
}

// Test_ConnectingState_MarkStageFailed asserts MarkStageFailed flips the named
// stage to Failed WITHOUT entering the error phase (errMsg stays empty), is
// idempotent, no-ops on an unknown id, and leaves other stages untouched. This
// is the schema-load-failure path (T3 AD7): Objects renders ✗ while the connect
// still succeeds with an empty rail.
func Test_ConnectingState_MarkStageFailed(t *testing.T) {
	s := &ConnectingState{}
	s.SetConnectingStaged("prod", []Stage{
		{ID: StageAuth, Label: "Authenticated", Status: StageDone},
		{ID: StageObjects, Label: "Loading objects…", Status: StageActive},
	})

	s.MarkStageFailed(StageObjects)
	if got := s.stageByID(StageObjects).Status; got != StageFailed {
		t.Errorf("Objects Status = %v; want StageFailed", got)
	}
	if s.IsError() {
		t.Errorf("IsError() = true after MarkStageFailed; want false (no errMsg set)")
	}
	if got := s.stageByID(StageAuth).Status; got != StageDone {
		t.Errorf("Auth Status = %v; want StageDone (unaffected)", got)
	}

	// Idempotent repeat + unknown-id no-op.
	s.MarkStageFailed(StageObjects)
	s.MarkStageFailed(StageID(999))
	if got := s.stageByID(StageObjects).Status; got != StageFailed {
		t.Errorf("Objects Status after repeat = %v; want StageFailed", got)
	}
	if len(s.stages) != 2 {
		t.Errorf("len(stages) = %d after unknown-id; want 2", len(s.stages))
	}

	want := "✓ Authenticated\n" +
		"✗ Loading objects…\n" +
		"Connecting to prod…"
	if got := s.BodyGlyph(' '); got != want {
		t.Errorf("BodyGlyph =\n%q\nwant\n%q", got, want)
	}
}

// Test_ConnectingState_SetStageLabel asserts SetStageLabel replaces a stage's
// display label (used to relabel Objects "Loading objects…" → "Loaded" on
// success), is a no-op on an unknown id, and combines with MarkStageDone to
// render "✓ Loaded N schemas" (T3 success criteria).
func Test_ConnectingState_SetStageLabel(t *testing.T) {
	s := &ConnectingState{}
	s.SetConnectingStaged("prod", []Stage{
		{ID: StageObjects, Label: "Loading objects…", Status: StageActive},
	})

	s.SetStageLabel(StageObjects, "Loaded")
	s.MarkStageDone(StageObjects, "3 schemas")
	s.SetStageLabel(StageID(999), "ignored") // no panic, no mutation

	want := "✓ Loaded 3 schemas\nConnecting to prod…"
	if got := s.BodyGlyph(' '); got != want {
		t.Errorf("BodyGlyph = %q; want %q", got, want)
	}
}

// Test_ConnectingState_SetErrorNoActive asserts SetError with no Active stage
// only sets errMsg (no panic, no spurious Failed flip).
func Test_ConnectingState_SetErrorNoActive(t *testing.T) {
	s := &ConnectingState{}
	s.SetConnectingStaged("prod", []Stage{
		{ID: StageTunnel, Label: "Tunnel", Status: StageDone},
		{ID: StageAuth, Label: "Authenticate", Status: StagePending},
	})
	s.SetError("upstream failure")

	if !s.IsError() {
		t.Fatalf("IsError() = false; want true")
	}
	for _, st := range s.stages {
		if st.Status == StageFailed {
			t.Errorf("stage %v unexpectedly Failed with no Active stage", st.ID)
		}
	}
}
