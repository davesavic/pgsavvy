//go:build !linux && !darwin

package main

// Other platforms retain the legacy entry path.
func runMachine([]string) (bool, int, func()) { return false, 0, nil }
