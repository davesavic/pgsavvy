package ui

import (
	"testing"
	"time"

	"github.com/spf13/afero"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/query"
)

func newHideTestHelper(t *testing.T) (*ResultTabsHelper, *common.AppStateStore) {
	t.Helper()
	store := common.NewAppStateStore(afero.NewMemMapFs(), "/tmp/state.yaml", common.DefaultClock())
	h := NewResultTabsHelper(ResultTabsHelperDeps{
		Toast: &fakeToaster{},
		Now:   time.Now,
		Store: store,
	})
	return h, store
}

func TestHideOverlay_OpenAndCloseSessionOnlyWhenNoIdentity(t *testing.T) {
	h, store := newHideTestHelper(t)
	_ = h.openTab("Q", nil)
	tab := h.Active()
	tab.Grid().SetColumns([]models.ColumnMeta{{Name: "a"}, {Name: "b"}})
	h.HideOverlay()
	if !h.HideOverlayActive() {
		t.Fatalf("HideOverlayActive should be true after HideOverlay()")
	}
	ov := h.HideOverlayState()
	if ov.PersistEnabled() {
		t.Errorf("PersistEnabled should be false without identity")
	}
	h.HideOverlayToggle()
	h.HideOverlayClose()
	if h.HideOverlayActive() {
		t.Errorf("HideOverlayActive should be false after close")
	}
	if got := tab.Grid().HiddenCols(); len(got) != 1 || !got[0] {
		t.Errorf("grid HiddenCols = %v; want {0:true}", got)
	}
	if got := store.HiddenColumnsSnapshot("connA", "schema.t"); got != nil {
		t.Errorf("persistence should be empty without identity; got %v", got)
	}
}

func TestHideOverlay_PersistsWhenHasRowIdentity(t *testing.T) {
	h, store := newHideTestHelper(t)
	_ = h.openTab("Q", nil)
	tab := h.Active()
	tab.Grid().SetColumns([]models.ColumnMeta{{Name: "a"}, {Name: "b"}, {Name: "c"}})
	tab.SetIdentity("connA", query.ResultIdentity{BaseTable: "public.t", HasRowIdentity: true})

	h.HideOverlay()
	h.HideOverlayToggle()
	h.HideOverlayMove(2)
	h.HideOverlayToggle()
	h.HideOverlayClose()
	got := store.HiddenColumnsSnapshot("connA", "public.t")
	if len(got) != 2 {
		t.Fatalf("persisted = %v; want 2 entries", got)
	}
	if got[0] != "a" || got[1] != "c" {
		t.Errorf("persisted = %v; want [a c]", got)
	}
}

func TestHideOverlay_TwoConnectionsIsolated(t *testing.T) {
	h, store := newHideTestHelper(t)
	_ = h.openTab("Q1", nil)
	t1 := h.Active()
	t1.Grid().SetColumns([]models.ColumnMeta{{Name: "x"}, {Name: "y"}})
	t1.SetIdentity("A", query.ResultIdentity{BaseTable: "t", HasRowIdentity: true})
	h.HideOverlay()
	h.HideOverlayToggle()
	h.HideOverlayClose()

	_ = h.openTab("Q2", nil)
	t2 := h.Active()
	t2.Grid().SetColumns([]models.ColumnMeta{{Name: "x"}, {Name: "y"}})
	t2.SetIdentity("B", query.ResultIdentity{BaseTable: "t", HasRowIdentity: true})

	if got := store.HiddenColumnsSnapshot("A", "t"); len(got) != 1 || got[0] != "x" {
		t.Errorf("conn A persisted = %v; want [x]", got)
	}
	if got := store.HiddenColumnsSnapshot("B", "t"); got != nil {
		t.Errorf("conn B persisted should be nil; got %v", got)
	}
}

func TestHideOverlay_SeedFromAppStateTranslatesNamesToIndices(t *testing.T) {
	h, store := newHideTestHelper(t)
	store.MutateAndSave(func(s *common.AppState) {
		s.HiddenColumns = map[string]map[string][]string{
			"A": {"t": {"b", "missing"}},
		}
	})

	_ = h.openTab("Q", nil)
	tab := h.Active()
	tab.Grid().SetColumns([]models.ColumnMeta{{Name: "a"}, {Name: "b"}, {Name: "c"}})
	tab.SetIdentity("A", query.ResultIdentity{BaseTable: "t", HasRowIdentity: true})

	h.SeedHiddenColsFromAppState(tab)
	got := tab.Grid().HiddenCols()
	if len(got) != 1 || !got[1] {
		t.Errorf("HiddenCols = %v; want {1:true}", got)
	}
}

func TestHideOverlay_EmptySetPrunesPersistedEntry(t *testing.T) {
	h, store := newHideTestHelper(t)
	store.MutateAndSave(func(s *common.AppState) {
		s.HiddenColumns = map[string]map[string][]string{
			"A": {"t": {"a"}},
		}
	})

	_ = h.openTab("Q", nil)
	tab := h.Active()
	tab.Grid().SetColumns([]models.ColumnMeta{{Name: "a"}, {Name: "b"}})
	tab.SetIdentity("A", query.ResultIdentity{BaseTable: "t", HasRowIdentity: true})
	h.SeedHiddenColsFromAppState(tab)

	h.HideOverlay()
	h.HideOverlayToggle()
	h.HideOverlayClose()
	if got := store.HiddenColumnsSnapshot("A", "t"); got != nil {
		t.Errorf("expected prune; got %v", got)
	}
}

func TestHideOverlay_MinimumOneVisibleRejected(t *testing.T) {
	h, _ := newHideTestHelper(t)
	_ = h.openTab("Q", nil)
	tab := h.Active()
	tab.Grid().SetColumns([]models.ColumnMeta{{Name: "a"}, {Name: "b"}})

	h.HideOverlay()
	h.HideOverlayToggle()
	h.HideOverlayMove(1)
	h.HideOverlayToggle()
	h.HideOverlayClose()
	if got := tab.Grid().HiddenCols(); len(got) != 1 || !got[0] {
		t.Errorf("HiddenCols = %v; want {0:true}", got)
	}
}
