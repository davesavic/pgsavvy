package grid

import (
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/theme"
)

// TestDecorateDirtyCell_NotDirtyReturnsValueUnchanged proves the
// zero-decoration path: when isDirty is false the input value is
// returned verbatim with no glyph and no ANSI escapes.
func TestDecorateDirtyCell_NotDirtyReturnsValueUnchanged(t *testing.T) {
	got := DecorateDirtyCell("hello", false, theme.Style{})
	if got != "hello" {
		t.Fatalf("got %q, want %q (no decoration when isDirty=false)", got, "hello")
	}
	if strings.ContainsRune(got, 0x1b) {
		t.Fatalf("got %q must not contain ANSI escapes when isDirty=false", got)
	}
}

// TestDecorateDirtyCell_DirtyAppendsMarker proves the dirty path appends
// `●` to the value. The zero Style yields no ANSI escapes so the
// returned string is just `value●`.
func TestDecorateDirtyCell_DirtyAppendsMarker(t *testing.T) {
	got := DecorateDirtyCell("hello", true, theme.Style{})
	if !strings.HasSuffix(got, dirtyCellMarker) {
		t.Fatalf("got %q, want suffix %q", got, dirtyCellMarker)
	}
	if !strings.Contains(got, "hello") {
		t.Fatalf("got %q, want substring %q", got, "hello")
	}
}

// TestDecorateDirtyCell_StyleAppliesAnsiEscape proves a non-zero style
// produces an ANSI SGR wrapper around the value+glyph. Uses the same
// recognised colour name path as the cell renderer.
func TestDecorateDirtyCell_StyleAppliesAnsiEscape(t *testing.T) {
	got := DecorateDirtyCell("v", true, theme.Style{Fg: "red"})
	want := "\x1b[31mv" + dirtyCellMarker + "\x1b[0m"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestRowHasPendingEdit_TableDriven covers the row-gutter matching
// matrix: nil set, empty set, no match, single match, composite-PK
// match, composite-PK partial mismatch.
func TestRowHasPendingEdit_TableDriven(t *testing.T) {
	type tc struct {
		name  string
		set   func() *models.PendingEditSet
		rowPK []any
		want  bool
	}

	withEdit := func(pk []any, col string) func() *models.PendingEditSet {
		return func() *models.PendingEditSet {
			s := &models.PendingEditSet{Table: models.Ref{Schema: "public", Table: "t"}}
			if err := s.Add(models.PendingEdit{PrimaryKey: pk, Column: col, NewValue: "x"}); err != nil {
				t.Fatalf("Add: %v", err)
			}
			return s
		}
	}

	cases := []tc{
		{"nil set", func() *models.PendingEditSet { return nil }, []any{1}, false},
		{"empty set", func() *models.PendingEditSet { return &models.PendingEditSet{} }, []any{1}, false},
		{"no rowPK", withEdit([]any{1}, "name"), nil, false},
		{"single PK match", withEdit([]any{int64(1)}, "name"), []any{int64(1)}, true},
		{"single PK no match", withEdit([]any{int64(1)}, "name"), []any{int64(2)}, false},
		{"composite PK match", withEdit([]any{int64(1), "us"}, "name"), []any{int64(1), "us"}, true},
		{"composite PK partial mismatch", withEdit([]any{int64(1), "us"}, "name"), []any{int64(1), "uk"}, false},
		{"composite PK length mismatch", withEdit([]any{int64(1), "us"}, "name"), []any{int64(1)}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RowHasPendingEdit(c.set(), c.rowPK)
			if got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

// TestCellHasPendingEdit_TableDriven covers the per-cell match path
// used by renderCellWithDirty.
func TestCellHasPendingEdit_TableDriven(t *testing.T) {
	build := func() *models.PendingEditSet {
		s := &models.PendingEditSet{Table: models.Ref{Schema: "public", Table: "t"}}
		if err := s.Add(models.PendingEdit{PrimaryKey: []any{int64(1)}, Column: "name", NewValue: "x"}); err != nil {
			t.Fatalf("Add: %v", err)
		}
		return s
	}

	type tc struct {
		name   string
		set    *models.PendingEditSet
		rowPK  []any
		column string
		want   bool
	}
	cases := []tc{
		{"nil set", nil, []any{int64(1)}, "name", false},
		{"empty set", &models.PendingEditSet{}, []any{int64(1)}, "name", false},
		{"empty column", build(), []any{int64(1)}, "", false},
		{"empty rowPK", build(), nil, "name", false},
		{"row + col match", build(), []any{int64(1)}, "name", true},
		{"row match, col miss", build(), []any{int64(1)}, "age", false},
		{"row miss, col match", build(), []any{int64(2)}, "name", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CellHasPendingEdit(c.set, c.rowPK, c.column)
			if got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

// TestGutterMarker_ReturnsMOnlyWhenMatched proves the AC for the row
// gutter: "M" when at least one edit matches the row PK, empty
// otherwise.
func TestGutterMarker_ReturnsMOnlyWhenMatched(t *testing.T) {
	s := &models.PendingEditSet{}
	if err := s.Add(models.PendingEdit{PrimaryKey: []any{int64(7)}, Column: "name", NewValue: "x"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if got := GutterMarker(0, s, []any{int64(7)}); got != "M" {
		t.Fatalf("matching row: got %q, want %q", got, "M")
	}
	if got := GutterMarker(1, s, []any{int64(8)}); got != "" {
		t.Fatalf("non-matching row: got %q, want empty", got)
	}
	if got := GutterMarker(0, nil, []any{int64(7)}); got != "" {
		t.Fatalf("nil set: got %q, want empty", got)
	}
	if got := GutterMarker(0, s, nil); got != "" {
		t.Fatalf("nil rowPK: got %q, want empty", got)
	}
}

// TestViewSetPendingEdits_RoundTrip proves the accessor pair: install
// returns the same pointer; install of nil clears.
func TestViewSetPendingEdits_RoundTrip(t *testing.T) {
	v := NewView()
	if v.PendingEdits() != nil {
		t.Fatalf("fresh View must return nil PendingEdits")
	}
	s := &models.PendingEditSet{}
	v.SetPendingEdits(s)
	if got := v.PendingEdits(); got != s {
		t.Fatalf("got %p, want %p", got, s)
	}
	v.SetPendingEdits(nil)
	if v.PendingEdits() != nil {
		t.Fatalf("SetPendingEdits(nil) must clear")
	}
}

// TestRenderCellWithDirty_NotDirtyMatchesRenderCell proves the
// no-edit path is byte-identical to renderCell — guarantees the dirty
// decorator never alters non-edited cells.
func TestRenderCellWithDirty_NotDirtyMatchesRenderCell(t *testing.T) {
	col := models.ColumnMeta{Name: "name", TypeName: "text"}
	vis1, dec1 := renderCell("hello", col)
	vis2, dec2 := renderCellWithDirty("hello", col, false)
	if vis1 != vis2 || dec1 != dec2 {
		t.Fatalf("not-dirty renderCellWithDirty diverged from renderCell:\n  vis  %q vs %q\n  dec  %q vs %q",
			vis1, vis2, dec1, dec2)
	}
}

// TestRenderCellWithDirty_DirtyAppendsMarker proves the dirty path
// appends the marker to BOTH the visible and decorated strings so the
// width budgeter and the SGR wrapper stay in sync.
func TestRenderCellWithDirty_DirtyAppendsMarker(t *testing.T) {
	col := models.ColumnMeta{Name: "name", TypeName: "text"}
	vis, dec := renderCellWithDirty("hello", col, true)
	if !strings.HasSuffix(vis, dirtyCellMarker) {
		t.Fatalf("visible %q must end with %q", vis, dirtyCellMarker)
	}
	if !strings.Contains(dec, dirtyCellMarker) {
		t.Fatalf("decorated %q must contain %q", dec, dirtyCellMarker)
	}
}
