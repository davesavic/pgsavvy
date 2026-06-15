package orchestrator

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// updateBindingSnapshot regenerates the committed golden fixture instead of
// asserting against it. Run `go test ./pkg/gui/orchestrator/ -run
// TestBindingPopupRectSnapshot -update` after an intentional wiring change.
var updateBindingSnapshot = flag.Bool("update", false, "regenerate the binding+popup-rect golden snapshot")

const bindingSnapshotGolden = "testdata/binding_popup_rect_snapshot.golden"

// TestBindingPopupRectSnapshot is the D3 behaviour-preservation oracle. It
// serializes the FULL set of (ContextKey, key-chord, Mode, Scope) binding
// tuples from the current shipped-default wiring PLUS the popupRectFor rect
// dims for every ContextKey over a fixed 100×100 canvas, then asserts the
// live serialization is byte-identical to the committed golden fixture.
//
// A later refactor (D3a/D3b) that preserves behaviour produces a
// byte-identical snapshot; any drift in which chord maps to which
// (context, mode, scope) — or in any popup rect — fails this test.
//
// Intentionally EXCLUDED from the tuple: ShowInBar and every other cosmetic
// flag (Description/Tag/OpensMenu/Source/Origin/ActionID). A parallel task
// is changing ShowInBar and it MUST NOT perturb this oracle. The fields kept
// are exactly the behaviour-bearing dispatch coordinates plus the geometric
// popup-rect dims.
func TestBindingPopupRectSnapshot(t *testing.T) {
	got := buildBindingPopupRectSnapshot(t)

	goldenPath := filepath.FromSlash(bindingSnapshotGolden)

	if *updateBindingSnapshot {
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", goldenPath, err)
		}
		t.Logf("updated golden snapshot %s", goldenPath)
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with -update to generate)", goldenPath, err)
	}
	if got != string(want) {
		t.Errorf("binding+popup-rect snapshot drifted from golden %s.\n"+
			"If this change is intentional, regenerate with:\n"+
			"  go test ./pkg/gui/orchestrator/ -run TestBindingPopupRectSnapshot -update\n"+
			"got:\n%s\nwant:\n%s", goldenPath, got, string(want))
	}
}

// buildBindingPopupRectSnapshot constructs a fully-wired Gui (mirroring the
// orchestrator's real wiring path) and serializes the deterministic snapshot.
func buildBindingPopupRectSnapshot(t *testing.T) string {
	t.Helper()

	// Mirror buildTestGuiWithLogger (gui_test.go): an in-memory fs, the
	// default config, a discard logger, and a recorder driver. This is the
	// faithful source of the shipped-default wiring (every controller is
	// constructed and AllDefaultBindings can be read off g.Controllers()).
	fs := afero.NewMemMapFs()
	cfg := config.GetDefaultConfig()
	c := common.NewCommon(slog.New(slog.DiscardHandler), i18n.EnglishTranslationSet(), cfg, &common.AppState{}, fs)
	store := common.NewAppStateStore(fs, "/tmp/state.yml", common.DefaultClock())

	g := NewGui(Deps{
		Common:              c,
		Store:               store,
		ConnectionsPath:     "/tmp/connections.yml",
		ConnectionsProvider: func() []models.Connection { return nil },
		DriverNamesFn:       func() []string { return []string{"postgres"} },
	})
	rec := testfake.NewRecorderGuiDriver()
	if err := g.UseDriverForTest(rec); err != nil {
		t.Fatalf("UseDriverForTest: %v", err)
	}
	t.Cleanup(func() { _ = g.Close() })

	var lines []string

	// Section 1: binding tuples. One line per (Scope, key-chord, Mode) — the
	// behaviour-bearing dispatch coordinates only.
	defaults := controllers.AllDefaultBindings(g.Controllers())
	if len(defaults) == 0 {
		t.Fatal("AllDefaultBindings returned no bindings on a wired Gui")
	}
	bindingLines := make([]string, 0, len(defaults))
	for _, b := range defaults {
		if b == nil {
			continue
		}
		bindingLines = append(bindingLines,
			fmt.Sprintf("binding\tscope=%s\tkey=%s\tmode=%s",
				b.Scope, types.SequenceString(b.Sequence), b.Mode))
	}
	// Deterministic ordering: sort by (scope, mode, key) — the full line
	// already encodes scope, key, mode in that field order, so a plain
	// string sort yields a stable, run-to-run-identical ordering.
	sort.Strings(bindingLines)
	lines = append(lines, bindingLines...)

	// Section 2: popup-rect dims for every ContextKey over a fixed 100×100
	// canvas (the canvas the existing popup_rect_for_test / wiring_invariant
	// tests use). Record dims when popupRectFor returns ok; a sentinel
	// otherwise. AllContextKeys() is already a fixed-order slice, but sort
	// for belt-and-braces determinism.
	canvas := ui.Dimensions{X0: 0, Y0: 0, X1: 100, Y1: 100}
	dims := map[string]ui.Dimensions{"popup-overlay": canvas}
	rectLines := make([]string, 0)
	for _, key := range types.AllContextKeys() {
		r, ok := popupRectFor(key, dims, 100, 100)
		if ok {
			rectLines = append(rectLines,
				fmt.Sprintf("popuprect\tkey=%s\tx0=%d\ty0=%d\tx1=%d\ty1=%d",
					key, r.X0, r.Y0, r.X1, r.Y1))
		} else {
			rectLines = append(rectLines, fmt.Sprintf("popuprect\tkey=%s\tnone", key))
		}
	}
	sort.Strings(rectLines)
	lines = append(lines, rectLines...)

	return strings.Join(lines, "\n") + "\n"
}
