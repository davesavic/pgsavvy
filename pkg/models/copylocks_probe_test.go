//go:build copylocks_probe

package models

// This file exists to make the `go vet copylocks` coverage on Table
// observable. Without it the AC "value-copy of Table is detected" would be
// tautological (vet only fires when copying code exists). Gated behind a
// build tag so it never affects normal `go vet`; CI runs:
//
//	go vet -tags=copylocks_probe ./pkg/models/...
//
// and asserts a non-zero exit.
func copylocksProbe() {
	var t Table
	cp := t //nolint:govet // intentional copy to trigger copylocks
	_ = cp
}
