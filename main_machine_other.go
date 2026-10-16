//go:build !linux

package main

// Machine commands are Linux-only. Other platforms retain the legacy entry path.
func runMachine([]string) (bool, int) { return false, 0 }
