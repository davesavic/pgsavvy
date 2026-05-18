//go:build integration

// Package orchestrator_test (integration) exercises the
// ResultTabsHelper end-to-end through the wired *orchestrator.Gui to
// satisfy dbsavvy-66p.12 AC ("Smoke: opens 9 tabs sequentially,
// asserts eviction order").
//
// The test is integration-tagged to keep it out of the default `task
// test` run; invoke via `task test:integration -- -run 'TestResultTabs'`.
package orchestrator_test

import (
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/afero"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/dbsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// setupResultTabsSmoke spins up a minimal wired Gui with the recorder
// driver so we exercise the real ResultTabsHelper + ResultTabsController
// graph without standing up a tcell screen.
func setupResultTabsSmoke(t *testing.T) *orchestrator.Gui {
	t.Helper()
	fs := afero.NewMemMapFs()
	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)
	cfg := config.GetDefaultConfig()
	tr := i18n.EnglishTranslationSet()
	c := common.NewCommon(log, tr, cfg, &common.AppState{}, fs)
	store := common.NewAppStateStore(fs, "/state/state.yml", common.DefaultClock())

	g := orchestrator.NewGui(orchestrator.Deps{
		Common:              c,
		Store:               store,
		ConnectionsPath:     "/cfg/connections.yml",
		ConnectionsProvider: func() []models.Connection { return nil },
		DriverNamesFn:       func() []string { return []string{"postgres"} },
	})
	rec := testfake.NewRecorderGuiDriver()
	if err := g.UseDriverForTest(rec); err != nil {
		t.Fatalf("UseDriverForTest: %v", err)
	}
	rec.SetManager(g)
	t.Cleanup(func() {
		_ = g.Close()
	})
	return g
}

// resultTabsHelper extracts the live *ui.ResultTabsHelper from the wired
// Gui. Returns nil when the helper wasn't wired (regression in
// orchestrator.wireWithDriver).
func resultTabsHelper(g *orchestrator.Gui) *ui.ResultTabsHelper {
	return g.ResultTabsHelper()
}

// TestResultTabsSmokeNineSequentialOpensEvictsOldestNonPinned exercises
// the eviction AC: open eight tabs to fill the cap, then open a ninth
// and confirm tab 0 (oldest non-pinned) was disposed and the new tab
// fills the freed slot.
func TestResultTabsSmokeNineSequentialOpensEvictsOldestNonPinned(t *testing.T) {
	g := setupResultTabsSmoke(t)
	h := resultTabsHelper(g)
	if h == nil {
		t.Fatal("ResultTabsHelper not wired into orchestrator.Gui")
	}
	if h.Max() != 8 {
		t.Fatalf("Max() = %d, want default 8", h.Max())
	}

	// Open eight tabs: t0..t7. nil RunHandle is fine — we're testing
	// tab-management, not streaming.
	for i := 0; i < 8; i++ {
		label := tabLabel(i)
		if err := h.OpenResultTab(label, nil); err != nil {
			t.Fatalf("OpenResultTab %q: %v", label, err)
		}
	}
	if h.Count() != 8 {
		t.Fatalf("Count after 8 opens = %d, want 8", h.Count())
	}

	// Snapshot pre-eviction labels in slot order.
	preLabels := labelsBySlot(h)
	if len(preLabels) != 8 {
		t.Fatalf("labels = %v, want 8 entries", preLabels)
	}
	for i := 0; i < 8; i++ {
		if preLabels[i] != tabLabel(i) {
			t.Errorf("slot %d label = %q, want %q", i, preLabels[i], tabLabel(i))
		}
	}

	// Open ninth tab: triggers eviction of oldest non-pinned (slot 0).
	if err := h.OpenResultTab("t8", nil); err != nil {
		t.Fatalf("OpenResultTab 'tab9': %v", err)
	}

	if h.Count() != 8 {
		t.Fatalf("Count after eviction = %d, want 8", h.Count())
	}

	// Verify slot 0 was reused by the new tab; t0 is gone; t1..t7 + t8.
	postLabels := labelsBySlot(h)
	wantSet := map[string]bool{
		"t1": false, "t2": false, "t3": false, "t4": false,
		"t5": false, "t6": false, "t7": false, "t8": false,
	}
	for _, lbl := range postLabels {
		if _, ok := wantSet[lbl]; ok {
			wantSet[lbl] = true
		}
	}
	for lbl, seen := range wantSet {
		if !seen {
			t.Errorf("expected tab %q to be present after eviction, missing", lbl)
		}
	}
	for _, lbl := range postLabels {
		if lbl == "t0" {
			t.Errorf("evicted tab 't0' is still present: %v", postLabels)
		}
	}

	// The new tab "t8" should occupy slot 0 (the freed slot).
	if postLabels[0] != "t8" {
		t.Errorf("slot 0 after eviction = %q, want 't8'", postLabels[0])
	}
}

// TestResultTabsSmokeAllPinnedRejectsOpen confirms ErrTabCapReached
// surfaces when every tab is pinned.
func TestResultTabsSmokeAllPinnedRejectsOpen(t *testing.T) {
	g := setupResultTabsSmoke(t)
	h := resultTabsHelper(g)
	if h == nil {
		t.Fatal("ResultTabsHelper not wired into orchestrator.Gui")
	}
	for i := 0; i < 8; i++ {
		_ = h.OpenResultTab(tabLabel(i), nil)
	}
	for _, tab := range h.Tabs() {
		h.Pin(tab)
	}
	err := h.OpenResultTab("blocked", nil)
	if err == nil {
		t.Fatal("OpenResultTab succeeded with all pinned at cap; want error")
	}
	if !strings.Contains(err.Error(), "cap") {
		t.Fatalf("err = %v, want substring 'cap'", err)
	}
	if h.Count() != 8 {
		t.Errorf("Count after rejected open = %d, want 8", h.Count())
	}
}

func tabLabel(i int) string {
	return "t" + itoa(i)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	var buf [16]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func labelsBySlot(h *ui.ResultTabsHelper) []string {
	tabs := h.Tabs()
	out := make([]string, len(tabs))
	for i, t := range tabs {
		out[i] = t.Label()
	}
	return out
}

// Keep time import live; setup ctor uses default key delays so the
// import would otherwise need removing on a refactor.
var _ = time.Now
