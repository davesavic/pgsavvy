package config

import (
	"testing"
	"time"
)

// TestGetDefaultConfigUIMouseEnabled pins the default per dbsavvy-zro AC.
// Mouse is opt-out: the bootstrap-time mouse-binding registration block
// in pkg/gui/controllers/* skips entirely when this flag is false, so
// the default MUST stay true to preserve documented UX.
func TestGetDefaultConfigUIMouseEnabled(t *testing.T) {
	if !GetDefaultConfig().UI.Mouse.Enabled {
		t.Fatal("GetDefaultConfig().UI.Mouse.Enabled = false; want true")
	}
}

// TestGetDefaultConfigLeaderDefaults pins the dbsavvy-dlp.3 defaults per
// the review-plan D19 amendment.
func TestGetDefaultConfigLeaderDefaults(t *testing.T) {
	cfg := GetDefaultConfig()
	if cfg.Leader != " " {
		t.Errorf("Leader = %q, want %q (space per D19)", cfg.Leader, " ")
	}
	if cfg.LocalLeader != "," {
		t.Errorf("LocalLeader = %q, want %q", cfg.LocalLeader, ",")
	}
}

func TestGetDefaultConfigTimeoutDefaults(t *testing.T) {
	cfg := GetDefaultConfig()
	if cfg.TimeoutLen != 1*time.Second {
		t.Errorf("TimeoutLen = %v, want 1s", cfg.TimeoutLen)
	}
	if cfg.TtimeoutLen != 50*time.Millisecond {
		t.Errorf("TtimeoutLen = %v, want 50ms", cfg.TtimeoutLen)
	}
	if cfg.WhichKeyDelay != 300*time.Millisecond {
		t.Errorf("WhichKeyDelay = %v, want 300ms", cfg.WhichKeyDelay)
	}
}
