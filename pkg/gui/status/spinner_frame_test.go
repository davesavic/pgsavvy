package status

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/i18n"
)

// TestBuildStatusLine_SpinnerAdvancesByFrameNotBusyCount verifies the
// spinner glyph is selected by the supplied frame counter, NOT by the
// busy worker count. A single long-running worker (busyCount fixed at 1)
// must still cycle through distinct glyphs as the frame counter advances
// (U8 AC: spinner advances by ELAPSED FRAMES, not worker count).
func TestBuildStatusLine_SpinnerAdvancesByFrameNotBusyCount(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	const busy = int64(1) // one worker the whole time

	seen := make(map[string]int)
	for frame := int64(0); frame < int64(len(spinnerGlyphs)); frame++ {
		line := BuildStatusLine("", nil, nil, tr, busy, frame, "", nil, nil)
		glyph := string(spinnerGlyphs[frame%int64(len(spinnerGlyphs))])
		if !containsRune(line, glyph) {
			t.Fatalf("frame %d: status line %q missing glyph %q", frame, line, glyph)
		}
		seen[glyph]++
	}
	if len(seen) < 2 {
		t.Fatalf("spinner did not advance across frames with fixed busyCount: distinct glyphs=%d, want >= 2", len(seen))
	}
}

// TestBuildStatusLine_NoSpinnerWhenQuiescent verifies busyCount<=0 hides
// the spinner regardless of frame value (the frame counter only selects
// the glyph; busyCount remains the show/hide gate).
func TestBuildStatusLine_NoSpinnerWhenQuiescent(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	for _, glyph := range spinnerGlyphs {
		line := BuildStatusLine("", nil, nil, tr, 0, 42, "", nil, nil)
		if containsRune(line, string(glyph)) {
			t.Fatalf("quiescent status line %q unexpectedly contains spinner glyph %q", line, string(glyph))
		}
	}
}

func containsRune(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
