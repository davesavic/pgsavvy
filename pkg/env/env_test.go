package env

import (
	"strings"
	"testing"

	"github.com/adrg/xdg"
)

func TestGetStateDir_ContainsAppName(t *testing.T) {
	got := GetStateDir()
	if got == "" {
		t.Fatal("GetStateDir returned empty path under normal env")
	}
	if !strings.Contains(got, "dbsavvy") {
		t.Fatalf("GetStateDir %q does not contain 'dbsavvy'", got)
	}
}

func TestGetConfigDir_ContainsAppName(t *testing.T) {
	got := GetConfigDir()
	if !strings.Contains(got, "dbsavvy") {
		t.Fatalf("GetConfigDir %q does not contain 'dbsavvy'", got)
	}
}

func TestGetCacheDir_ContainsAppName(t *testing.T) {
	got := GetCacheDir()
	if !strings.Contains(got, "dbsavvy") {
		t.Fatalf("GetCacheDir %q does not contain 'dbsavvy'", got)
	}
}

func TestGetenv_ReturnsValueWhenSet(t *testing.T) {
	t.Setenv("DBSAVVY_TEST_KEY", "real")
	if got := Getenv("DBSAVVY_TEST_KEY", "fallback"); got != "real" {
		t.Fatalf("got %q, want 'real'", got)
	}
}

func TestGetenv_ReturnsFallbackWhenUnset(t *testing.T) {
	t.Setenv("DBSAVVY_NONEXISTENT_KEY_XYZ", "")
	if got := Getenv("DBSAVVY_NONEXISTENT_KEY_XYZ", "fallback"); got != "fallback" {
		t.Fatalf("got %q, want 'fallback'", got)
	}
}

func TestGetenv_FallbackWhenEmpty(t *testing.T) {
	t.Setenv("DBSAVVY_EMPTY_KEY", "")
	if got := Getenv("DBSAVVY_EMPTY_KEY", "fallback"); got != "fallback" {
		t.Fatalf("got %q, want 'fallback'", got)
	}
}

// TestGetStateDir_RespectsXDGOverride confirms that xdg.Reload picks up env
// overrides — necessary precondition for the logs package tests.
func TestGetStateDir_RespectsXDGOverride(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/tmp/dbsavvy-xdg-state-test")
	xdg.Reload()
	t.Cleanup(func() { xdg.Reload() })
	got := GetStateDir()
	if got != "/tmp/dbsavvy-xdg-state-test/dbsavvy" {
		t.Fatalf("got %q, want '/tmp/dbsavvy-xdg-state-test/dbsavvy'", got)
	}
}
