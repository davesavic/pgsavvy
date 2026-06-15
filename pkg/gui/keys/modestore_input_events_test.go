package keys

import (
	"bytes"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

func TestModeStore_Set_EmitsModeSetEvent(t *testing.T) {
	buf := &bytes.Buffer{}
	s := NewModeStore()
	s.SetSessionLog(newCapturingLogger(buf))

	s.Set(types.QUERY_EDITOR, types.ModeInsert)
	evs := findEvents(t, buf, "mode_set")
	if len(evs) != 1 {
		t.Fatalf("mode_set events = %d, want 1\nbuf=%s", len(evs), buf.String())
	}
	e := evs[0]
	if e["cat"] != "input" {
		t.Errorf("cat = %v, want input", e["cat"])
	}
	if e["ctx"] != string(types.QUERY_EDITOR) {
		t.Errorf("ctx = %v, want %s", e["ctx"], types.QUERY_EDITOR)
	}
	if e["old_mode"] != types.ModeNormal.String() {
		t.Errorf("old_mode = %v, want %s", e["old_mode"], types.ModeNormal.String())
	}
	if e["new_mode"] != types.ModeInsert.String() {
		t.Errorf("new_mode = %v, want %s", e["new_mode"], types.ModeInsert.String())
	}
}

func TestModeStore_Reset_EmitsModeResetEvent(t *testing.T) {
	buf := &bytes.Buffer{}
	s := NewModeStore()
	s.SetSessionLog(newCapturingLogger(buf))

	s.Set(types.QUERY_EDITOR, types.ModeInsert)
	buf.Reset()
	s.Reset(types.QUERY_EDITOR)

	evs := findEvents(t, buf, "mode_reset")
	if len(evs) != 1 {
		t.Fatalf("mode_reset events = %d, want 1\nbuf=%s", len(evs), buf.String())
	}
	e := evs[0]
	if e["ctx"] != string(types.QUERY_EDITOR) {
		t.Errorf("ctx = %v, want %s", e["ctx"], types.QUERY_EDITOR)
	}
	if e["old_mode"] != types.ModeInsert.String() {
		t.Errorf("old_mode = %v, want %s", e["old_mode"], types.ModeInsert.String())
	}
	if e["new_mode"] != types.ModeNormal.String() {
		t.Errorf("new_mode = %v, want %s", e["new_mode"], types.ModeNormal.String())
	}
}

func TestModeStore_NilSessionLog_NoPanic(t *testing.T) {
	s := NewModeStore()
	// SetSessionLog not called.
	s.Set(types.QUERY_EDITOR, types.ModeInsert)
	s.Reset(types.QUERY_EDITOR)
}
