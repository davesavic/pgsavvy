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

// TestGetDefaultConfigUIPaginationDefaults pins the dbsavvy-uv0.3
// pagination knob defaults: 200/50/25/1_000_000.
func TestGetDefaultConfigUIPaginationDefaults(t *testing.T) {
	cfg := GetDefaultConfig()
	if cfg.UI.ResultPageSize != 200 {
		t.Errorf("UI.ResultPageSize = %d, want 200", cfg.UI.ResultPageSize)
	}
	if cfg.UI.ResultPrefetchRows != 50 {
		t.Errorf("UI.ResultPrefetchRows = %d, want 50", cfg.UI.ResultPrefetchRows)
	}
	if cfg.UI.PrefetchThreshold != 25 {
		t.Errorf("UI.PrefetchThreshold = %d, want 25", cfg.UI.PrefetchThreshold)
	}
	if cfg.UI.ReadToEndWarnThreshold != 1_000_000 {
		t.Errorf("UI.ReadToEndWarnThreshold = %d, want 1_000_000", cfg.UI.ReadToEndWarnThreshold)
	}
}

// TestGetDefaultConfigUIExportDefaults pins the dbsavvy-uv0.9 export
// knob defaults: 100_000 buffered-row warn threshold and 16 MiB
// clipboard cap.
func TestGetDefaultConfigUIExportDefaults(t *testing.T) {
	cfg := GetDefaultConfig()
	if cfg.UI.Export.BufferedRowWarnThreshold != 100_000 {
		t.Errorf("UI.Export.BufferedRowWarnThreshold = %d, want 100_000", cfg.UI.Export.BufferedRowWarnThreshold)
	}
	if cfg.UI.Export.ClipboardMaxBytes != 16*1024*1024 {
		t.Errorf("UI.Export.ClipboardMaxBytes = %d, want %d (16 MiB)", cfg.UI.Export.ClipboardMaxBytes, 16*1024*1024)
	}
}
