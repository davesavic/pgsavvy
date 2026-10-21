//go:build !linux && !darwin

package main

import "testing"

func TestMachineCLIUnsupportedKeepsLegacyRoute(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"capabilities"}, {"connections", "list"}} {
		if handled, exit := runMachine(args); handled || exit != 0 {
			t.Fatal("unsupported platform unexpectedly activated machine commands")
		}
	}
}
