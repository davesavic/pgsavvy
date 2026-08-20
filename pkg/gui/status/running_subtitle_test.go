package status

import (
	"strings"
	"testing"
	"time"
)

// TestFormatElapsed proves the three-branch elapsed rendering and its
// exact boundary behavior: 60s enters the minutes branch, 60m enters
// the hours branch, and sub-minute tenths are floored (never rounded
// up to an unreached boundary).
func TestFormatElapsed(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{name: "negative returns empty", d: -time.Second, want: ""},
		{name: "zero", d: 0, want: "0.0s"},
		{name: "999ms floors to nine tenths", d: 999 * time.Millisecond, want: "0.9s"},
		{name: "whole seconds keep trailing zero", d: 4 * time.Second, want: "4.0s"},
		{name: "fractional seconds", d: 58*time.Second + 700*time.Millisecond, want: "58.7s"},
		{name: "59.9s stays in tenths branch", d: 59*time.Second + 900*time.Millisecond, want: "59.9s"},
		{name: "60s boundary enters minutes", d: 60 * time.Second, want: "1m00s"},
		{name: "single-digit seconds zero-padded", d: 63 * time.Second, want: "1m03s"},
		{name: "59m59s", d: 59*time.Minute + 59*time.Second, want: "59m59s"},
		{name: "60m boundary enters hours", d: 60 * time.Minute, want: "1h00m"},
		{name: "2h zero-pads minutes", d: 2 * time.Hour, want: "2h00m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatElapsed(tt.d); got != tt.want {
				t.Fatalf("FormatElapsed(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}

// TestBuildRunningSubtitle_JoinsGlyphAndElapsed proves the subtitle is
// exactly the frame's spinner glyph, a single space, then the formatted
// elapsed time.
func TestBuildRunningSubtitle_JoinsGlyphAndElapsed(t *testing.T) {
	got := BuildRunningSubtitle(3, 62*time.Second+500*time.Millisecond)
	if !strings.Contains(got, "1m02s") {
		t.Fatalf("subtitle %q missing elapsed %q", got, "1m02s")
	}
	want := string(SpinnerGlyph(3)) + " " + "1m02s"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestBuildRunningSubtitle_NegativeElapsedReturnsEmpty proves the
// no-slot contract: a negative elapsed (clock skew, unset start) yields
// "" so callers can unconditionally append the return.
func TestBuildRunningSubtitle_NegativeElapsedReturnsEmpty(t *testing.T) {
	if got := BuildRunningSubtitle(0, -time.Millisecond); got != "" {
		t.Fatalf("got %q, want empty for negative elapsed", got)
	}
}

// TestBuildRunningSubtitle_HugeFrameWrapsSafely proves an out-of-range
// frame counter (wrapped int64 ticker) selects a valid glyph via the
// sign-safe modulo instead of panicking, and the subtitle stays
// well-formed.
func TestBuildRunningSubtitle_HugeFrameWrapsSafely(t *testing.T) {
	got := BuildRunningSubtitle(1000000, 0)
	want := string(SpinnerGlyph(1000000)) + " " + "0.0s"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
