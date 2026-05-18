package tasks_test

import (
	"runtime"
	"strconv"
	"strings"
)

// currentGoroutineID returns the runtime-assigned id of the calling
// goroutine, parsed out of the first line of runtime.Stack. Test-only
// helper for the "appendRows always on the UI goroutine" assertion in
// TestNewQueryTaskAppendRowsAlwaysOnUIThread; the manager itself never
// inspects goroutine ids.
//
// The runtime guarantees the "goroutine N [...]" prefix on the first
// line of every stack dump; this has been stable across every Go
// release that supports modules. Using it inside test code is
// idiomatic (lazygit and many other projects rely on the same parse).
func currentGoroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	// Expected prefix: "goroutine 123 [running]:"
	line := string(buf[:n])
	const prefix = "goroutine "
	if !strings.HasPrefix(line, prefix) {
		return 0
	}
	rest := line[len(prefix):]
	sp := strings.IndexByte(rest, ' ')
	if sp <= 0 {
		return 0
	}
	id, err := strconv.ParseUint(rest[:sp], 10, 64)
	if err != nil {
		return 0
	}
	return id
}
