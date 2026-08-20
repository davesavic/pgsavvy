//go:build race

// Package race reports whether the race detector was enabled at build
// time, letting tests scale latency budgets that the detector's
// instrumentation overhead would otherwise blow through.
package race

// Enabled is true when the binary was built with -race.
const Enabled = true
