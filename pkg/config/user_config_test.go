package config

import "testing"

// TestGetDefaultConfigUIMouseEnabled pins the default per dbsavvy-zro AC.
// Mouse is opt-out: the bootstrap-time mouse-binding registration block
// in pkg/gui/controllers/* skips entirely when this flag is false, so
// the default MUST stay true to preserve documented UX.
func TestGetDefaultConfigUIMouseEnabled(t *testing.T) {
	if !GetDefaultConfig().UI.Mouse.Enabled {
		t.Fatal("GetDefaultConfig().UI.Mouse.Enabled = false; want true")
	}
}
