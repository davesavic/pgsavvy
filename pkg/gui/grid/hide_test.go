package grid

import (
	"reflect"
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/models"
)

func TestFilterHidden_NilMapReturnsInputUnchanged(t *testing.T) {
	in := []int{0, 1, 2}
	out := filterHidden(in, nil)
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("filterHidden(nil) = %v; want %v", out, in)
	}
}

func TestFilterHidden_RemovesHiddenIndicesPreservingOrder(t *testing.T) {
	in := []int{0, 1, 2, 3, 4}
	out := filterHidden(in, map[int]bool{1: true, 3: true})
	want := []int{0, 2, 4}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("filterHidden = %v; want %v", out, want)
	}
}

func TestSetColumns_ClearsHiddenColSet(t *testing.T) {
	v := NewView()
	v.SetColumns([]models.ColumnMeta{{Name: "a"}, {Name: "b"}, {Name: "c"}})
	v.SetHiddenCols(map[int]bool{1: true})
	if got := v.HiddenCols(); len(got) != 1 {
		t.Fatalf("pre-clear HiddenCols = %v; want size 1", got)
	}
	v.SetColumns([]models.ColumnMeta{{Name: "x"}, {Name: "y"}})
	if got := v.HiddenCols(); len(got) != 0 {
		t.Fatalf("post-SetColumns HiddenCols = %v; want empty", got)
	}
}

func TestSetHiddenCols_DropsOutOfRangeIndices(t *testing.T) {
	v := NewView()
	v.SetColumns([]models.ColumnMeta{{Name: "a"}, {Name: "b"}})
	v.SetHiddenCols(map[int]bool{0: true, 5: true, -1: true})
	got := v.HiddenCols()
	want := map[int]bool{0: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("HiddenCols = %v; want %v", got, want)
	}
}

func TestHiddenColumnNames_OrderedByColumnIndex(t *testing.T) {
	v := NewView()
	v.SetColumns([]models.ColumnMeta{{Name: "a"}, {Name: "b"}, {Name: "c"}})
	v.SetHiddenCols(map[int]bool{2: true, 0: true})
	got := v.HiddenColumnNames()
	want := []string{"a", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("HiddenColumnNames = %v; want %v", got, want)
	}
}

func TestVisibleColumnOrder_SkipsHiddenColumns(t *testing.T) {
	v := NewView()
	v.SetColumns([]models.ColumnMeta{{Name: "a"}, {Name: "b"}, {Name: "c"}})
	v.SetHiddenCols(map[int]bool{1: true})
	snap := v.snapshot()
	got := visibleColumnOrder(snap)
	want := []int{0, 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("visibleColumnOrder = %v; want %v", got, want)
	}
}

func TestVisibleColumnOrder_FrozenFirstWithHidden(t *testing.T) {
	v := NewView()
	v.SetColumns([]models.ColumnMeta{{Name: "a"}, {Name: "b"}, {Name: "c"}})
	v.ToggleFrozenFirstColumn()
	v.SetHiddenCols(map[int]bool{1: true})
	snap := v.snapshot()
	got := visibleColumnOrder(snap)
	want := []int{0, 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("visibleColumnOrder = %v; want %v", got, want)
	}
}

func TestHideFooterLine_RendersDimNamesWhenAnyHidden(t *testing.T) {
	v := NewView()
	v.SetColumns([]models.ColumnMeta{{Name: "a"}, {Name: "b"}, {Name: "c"}})
	v.SetHiddenCols(map[int]bool{0: true, 2: true})
	snap := v.snapshot()
	footer := hideFooterLine(snap)
	if !strings.Contains(footer, "hidden: a, c") {
		t.Errorf("footer = %q; want to contain 'hidden: a, c'", footer)
	}
	if !strings.Contains(footer, "\x1b[2m") || !strings.Contains(footer, "\x1b[22m") {
		t.Errorf("footer = %q; want dim SGR wrap", footer)
	}
}

func TestHideFooterLine_EmptyWhenNothingHidden(t *testing.T) {
	v := NewView()
	v.SetColumns([]models.ColumnMeta{{Name: "a"}})
	snap := v.snapshot()
	if got := hideFooterLine(snap); got != "" {
		t.Errorf("footer = %q; want empty", got)
	}
}
