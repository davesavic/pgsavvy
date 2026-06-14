// Package integration_test contains cross-package smoke tests that wire
// together packages from different layers of the dbsavvy architecture. These
// tests are intentionally placed outside pkg/ so they cannot accidentally
// import unexported symbols and so they exercise only the public API surface
// each downstream epic will consume.
package integration_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/theme"
	"github.com/davesavic/dbsavvy/pkg/theme/builtin"
	"github.com/davesavic/dbsavvy/pkg/utils"
)

// TestFoundationSmoke wires every foundation package together via its public
// API and asserts the round-trip is healthy. It is the closing gate for the
// the foundation epic: downstream epics consume these exact surfaces.
func TestFoundationSmoke(t *testing.T) {
	c := common.NewDummyCommon()
	if c == nil {
		t.Fatal("common.NewDummyCommon returned nil")
	}
	if c.Cfg() == nil {
		t.Fatal("c.Cfg() returned nil — UserConfig pointer not published")
	}
	if c.Fs == nil {
		t.Fatal("c.Fs is nil — dummy common did not wire afero.Fs")
	}
	if c.AppState == nil {
		t.Fatal("c.AppState is nil — dummy common did not wire AppState")
	}

	if err := theme.Apply(builtin.DefaultDark()); err != nil {
		t.Fatalf("theme.Apply(DefaultDark) returned error: %v", err)
	}
	if got := theme.Current(); got == nil || got.ActiveBorder == nil {
		t.Fatalf("theme.Current().ActiveBorder is nil after Apply; got snapshot=%+v", got)
	}

	const statePath = "/state.yml"
	// Populate the AppState with non-zero values across each field shape
	// (scalar, slice, nested map) so the round-trip exercises real
	// marshal/unmarshal and reflect.DeepEqual is not tripped by nil-vs-empty
	// map ambiguity on a zero-value struct.
	c.AppState.LastConnectionID = "conn-1"
	c.AppState.RecentConnectionIDs = []string{"conn-1"}
	c.AppState.LastBufferUUIDs = map[string]string{"conn-1": "buf-a"}
	c.AppState.HiddenSchemas = map[string][]string{"conn-1": {"_internal"}}
	c.AppState.HiddenColumns = map[string]map[string][]string{"conn-1": {"users": {"password"}}}
	c.AppState.StatementTimeoutOverride = map[string]string{"conn-1": "30s"}
	c.AppState.LastSessionSettings = map[string]map[string]string{"conn-1": {"search_path": "public"}}
	c.AppState.LastSchemaName = map[string]string{"conn-1": "public"}
	c.AppState.LastTableName = map[string]string{"conn-1": "users"}
	// populate the remaining scalar fields so the round-
	// trip exercises every AppState entry instead of only the maps.
	c.AppState.LastTheme = "default-dark"
	c.AppState.LastResultViewMode = "expanded"
	// Drop monotonic clock so reflect.DeepEqual round-trips through YAML.
	c.AppState.StartupTipsSeenAt = time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	c.AppState.Version = "v0.0.1"

	if err := c.AppState.Save(c.Fs, statePath); err != nil {
		t.Fatalf("AppState.Save returned error: %v", err)
	}
	loaded := &common.AppState{}
	if err := loaded.Load(c.Fs, statePath); err != nil {
		t.Fatalf("AppState.Load returned error: %v", err)
	}
	if !reflect.DeepEqual(loaded, c.AppState) {
		t.Fatalf("AppState round-trip mismatch:\n  saved=%+v\n  loaded=%+v", c.AppState, loaded)
	}

	tr, err := i18n.LoadAndMerge(c.Fs, "en", nil)
	if err != nil {
		t.Fatalf("i18n.LoadAndMerge returned error: %v", err)
	}
	if tr == nil {
		t.Fatal("i18n.LoadAndMerge returned nil *TranslationSet")
	}

	out, err := utils.ResolveTemplate("hello {{.}}", "world")
	if err != nil {
		t.Fatalf("utils.ResolveTemplate returned error: %v", err)
	}
	if out != "hello world" {
		t.Fatalf("utils.ResolveTemplate output mismatch: got %q want %q", out, "hello world")
	}
}
