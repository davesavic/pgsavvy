package editor

import "testing"

func TestRepeatStoreReplayUncaptured(t *testing.T) {
	var r RepeatStore
	if _, _, _, ok := r.Replay(); ok {
		t.Error("Replay() on zero RepeatStore returned ok=true; want false")
	}
}

func TestRepeatStoreCaptureAndReplay(t *testing.T) {
	var r RepeatStore
	r.Capture("operator.delete", "motion.word_next", "", 3, 'a')
	opID, count, reg, ok := r.Replay()
	if !ok {
		t.Fatal("Replay() after Capture returned ok=false")
	}
	if opID != "operator.delete" {
		t.Errorf("opID = %q; want operator.delete", opID)
	}
	if count != 3 {
		t.Errorf("count = %d; want 3", count)
	}
	if reg != 'a' {
		t.Errorf("reg = %q; want 'a'", reg)
	}
	if r.LastMotionID != "motion.word_next" {
		t.Errorf("LastMotionID = %q; want motion.word_next", r.LastMotionID)
	}
	if r.LastTextObjectID != "" {
		t.Errorf("LastTextObjectID = %q; want empty", r.LastTextObjectID)
	}
}

func TestRepeatStoreCaptureRejectsEmptyOpID(t *testing.T) {
	var r RepeatStore
	r.Capture("", "motion.word_next", "", 1, 0)
	if r.LastOpID != "" {
		t.Errorf("Capture with empty opID stored LastOpID=%q; want unchanged", r.LastOpID)
	}
	if _, _, _, ok := r.Replay(); ok {
		t.Error("Replay() after empty-opID Capture returned ok=true; want false")
	}
}

func TestRepeatStoreCaptureNilReceiver(t *testing.T) {
	var r *RepeatStore // intentionally nil
	// Must not panic.
	r.Capture("operator.delete", "", "", 1, 0)
}

func TestRepeatStoreReplayPreservesTextObjectID(t *testing.T) {
	var r RepeatStore
	r.Capture("operator.delete", "", "textobject.around_paragraph", 1, '"')
	if r.LastMotionID != "" {
		t.Errorf("LastMotionID = %q; want empty (textobject path)", r.LastMotionID)
	}
	if r.LastTextObjectID != "textobject.around_paragraph" {
		t.Errorf("LastTextObjectID = %q; want textobject.around_paragraph", r.LastTextObjectID)
	}
}
