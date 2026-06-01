package popup

import (
	"errors"
	"strings"
	"testing"
)

func TestHideOverlay_SetNamesUpdatesRenderedLabels(t *testing.T) {
	ov := NewHideOverlay([]string{"id", "id"}, nil, false)
	ov.SetNames([]string{"posts.id", "posts_summary.id"})
	body := ov.Body()
	if !strings.Contains(body, "posts.id") || !strings.Contains(body, "posts_summary.id") {
		t.Fatalf("body should show qualified names, got:\n%s", body)
	}
}

func TestHideOverlay_SetNamesLengthMismatchIsIgnored(t *testing.T) {
	ov := NewHideOverlay([]string{"a", "b"}, nil, false)
	ov.SetNames([]string{"only-one"})
	if got := ov.Names(); got[0] != "a" || got[1] != "b" {
		t.Fatalf("mismatched length must not mutate names, got %v", got)
	}
}

func TestHideOverlay_ToggleHidesAndUnhides(t *testing.T) {
	ov := NewHideOverlay([]string{"a", "b", "c"}, nil, true)
	if err := ov.Toggle(); err != nil {
		t.Fatalf("Toggle: %v", err)
	}
	if !ov.HiddenSet()[0] {
		t.Fatalf("cursor 0 should be hidden")
	}
	if err := ov.Toggle(); err != nil {
		t.Fatalf("Toggle: %v", err)
	}
	if len(ov.HiddenSet()) != 0 {
		t.Fatalf("HiddenSet should be empty after re-toggle")
	}
}

func TestHideOverlay_RejectsLastVisibleHide(t *testing.T) {
	ov := NewHideOverlay([]string{"a", "b"}, nil, true)
	if err := ov.Toggle(); err != nil {
		t.Fatalf("first toggle: %v", err)
	}
	ov.MoveCursor(1)
	err := ov.Toggle()
	if !errors.Is(err, ErrMinimumOneVisible) {
		t.Fatalf("expected ErrMinimumOneVisible; got %v", err)
	}
	if ov.HiddenSet()[1] {
		t.Fatalf("toggle must NOT mutate state on rejection")
	}
}

func TestHideOverlay_MoveCursorClamps(t *testing.T) {
	ov := NewHideOverlay([]string{"a", "b"}, nil, false)
	ov.MoveCursor(-5)
	if ov.Cursor() != 0 {
		t.Errorf("cursor = %d; want 0", ov.Cursor())
	}
	ov.MoveCursor(99)
	if ov.Cursor() != 1 {
		t.Errorf("cursor = %d; want 1", ov.Cursor())
	}
}

func TestHideOverlay_BodyFooterWhenNotPersisted(t *testing.T) {
	ov := NewHideOverlay([]string{"a"}, nil, false)
	if !strings.Contains(ov.Body(), "not persisted") {
		t.Error("Body() should contain not-persisted footer when persist=false")
	}
	ov2 := NewHideOverlay([]string{"a"}, nil, true)
	if strings.Contains(ov2.Body(), "not persisted") {
		t.Error("Body() should NOT contain footer when persist=true")
	}
}

func TestHideOverlay_BodyShowsCursorAndHideState(t *testing.T) {
	ov := NewHideOverlay([]string{"a", "b"}, map[int]bool{1: true}, true)
	body := ov.Body()
	if !strings.Contains(body, "> [ ] a") {
		t.Errorf("body should mark cursor 0 with > and unchecked: %q", body)
	}
	if !strings.Contains(body, "  [x] b") {
		t.Errorf("body should mark col 1 as hidden [x]: %q", body)
	}
}

func TestHideOverlay_InitialHiddenIsDefensivelyCopied(t *testing.T) {
	src := map[int]bool{0: true}
	ov := NewHideOverlay([]string{"a", "b"}, src, true)
	src[0] = false
	if !ov.HiddenSet()[0] {
		t.Error("overlay must own defensive copy of initial hidden set")
	}
}
