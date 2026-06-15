package ui

import (
	"context"
	"testing"
	"time"

	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/models"
)

func newHideQualifyHelper(t *testing.T, resolve func(context.Context, []uint32) (map[uint32]string, error)) *ResultTabsHelper {
	t.Helper()
	store := common.NewAppStateStore(afero.NewMemMapFs(), "/tmp/state.yaml", common.DefaultClock())
	return NewResultTabsHelper(ResultTabsHelperDeps{
		Toast:             &fakeToaster{},
		Now:               time.Now,
		Store:             store,
		ResolveTableNames: resolve,
	})
}

func TestHideOverlay_QualifiesNamesWhenMultiTable(t *testing.T) {
	h := newHideQualifyHelper(t, func(_ context.Context, oids []uint32) (map[uint32]string, error) {
		return map[uint32]string{100: "posts", 200: "posts_summary"}, nil
	})
	_ = h.openTab("Q", nil)
	tab := h.Active()
	tab.Grid().SetColumns([]models.ColumnMeta{
		{Name: "id", TableOID: 100},
		{Name: "id", TableOID: 200},
	})

	h.HideOverlay()

	got := h.HideOverlayState().Names()
	if len(got) != 2 || got[0] != "posts.id" || got[1] != "posts_summary.id" {
		t.Fatalf("Names = %v; want [posts.id posts_summary.id]", got)
	}
}

func TestHideOverlay_SingleTableSkipsResolution(t *testing.T) {
	calls := 0
	h := newHideQualifyHelper(t, func(_ context.Context, oids []uint32) (map[uint32]string, error) {
		calls++
		return nil, nil
	})
	_ = h.openTab("Q", nil)
	tab := h.Active()
	tab.Grid().SetColumns([]models.ColumnMeta{
		{Name: "id", TableOID: 100},
		{Name: "email", TableOID: 100},
	})

	h.HideOverlay()

	if calls != 0 {
		t.Errorf("resolver called %d times for single-table result; want 0", calls)
	}
	got := h.HideOverlayState().Names()
	if got[0] != "id" || got[1] != "email" {
		t.Errorf("Names = %v; want bare [id email]", got)
	}
}
