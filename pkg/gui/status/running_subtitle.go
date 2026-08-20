package status

import (
	"fmt"
	"time"
)

// FormatElapsed renders an elapsed time for the running-query subtitle.
// The return contract:
//
//   - d < 0                       → ""  (no slot)
//   - 0 ≤ d < 1min → tenths of a second, trailing zero kept:
//     "4.0s", "58.7s"
//   - 1min ≤ d < 60min → "MmSSs" with zero-padded seconds:
//     "1m03s", "59m59s"
//   - d ≥ 60min → "HhMMm" with zero-padded minutes:
//     "1h02m"
//
// Tenths are FLOORED, never rounded up (999ms → "0.9s"): a running
// clock must not display a boundary it has not yet reached. All
// arithmetic is integer math on the duration — no float formatting
// edge cases.
func FormatElapsed(d time.Duration) string {
	switch {
	case d < 0:
		return ""
	case d < time.Minute:
		tenths := d / (time.Second / 10)
		return fmt.Sprintf("%d.%ds", tenths/10, tenths%10)
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", d/time.Minute, (d%time.Minute)/time.Second)
	default:
		return fmt.Sprintf("%dh%02dm", d/time.Hour, (d%time.Hour)/time.Minute)
	}
}

// BuildRunningSubtitle renders the running-query subtitle: the braille
// spinner glyph for frame, a single space, then FormatElapsed(elapsed).
// The return contract:
//
//   - elapsed < 0 → ""  (no slot)
//   - otherwise   → string(SpinnerGlyph(frame)) + " " + FormatElapsed(elapsed)
//
// frame is masked into range by SpinnerGlyph's sign-safe modulo, so
// wrapped or huge frame values never index out of bounds. Pure
// function: no Gui/session deps, no package state, no i18n — the
// caller owns where and how the subtitle is displayed.
func BuildRunningSubtitle(frame int64, elapsed time.Duration) string {
	if elapsed < 0 {
		return ""
	}
	return string(SpinnerGlyph(frame)) + " " + FormatElapsed(elapsed)
}
