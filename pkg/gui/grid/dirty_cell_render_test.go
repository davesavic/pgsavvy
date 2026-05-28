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

// TestDecorateDirtyCell_DirtyZeroStyleUnchanged proves the dirty path with
// the zero Style yields no ANSI escapes and no glyph — the value is returned
// untouched (the edit is signalled by background colour alone). dbsavvy-kvk.
func TestDecorateDirtyCell_DirtyZeroStyleUnchanged(t *testing.T) {
	got := DecorateDirtyCell("hello", true, theme.Style{})
	if got != "hello" {
		t.Fatalf("got %q, want %q (no glyph, no escape for zero style)", got, "hello")
	}
}

// TestDecorateDirtyCell_StyleAppliesAnsiEscape proves a non-zero style
// produces an ANSI SGR wrapper around the value, with no per-cell glyph.
// Uses the same recognised colour name path as the cell renderer.
func TestDecorateDirtyCell_StyleAppliesAnsiEscape(t *testing.T) {
	got := DecorateDirtyCell("v", true, theme.Style{Fg: "red"})
	want := "\x1b[31mv\x1b[0m"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestAnsiBgCode maps named background colours to their basic SGR codes and
// `#RRGGBB` hex to truecolor (48;2;R;G;B); malformed/unknown tokens collapse
// to "" — the same fallback policy as ansiFgCode. dbsavvy-kvk.
func TestAnsiBgCode(t *testing.T) {
	cases := map[string]string{
		"black":       "\x1b[40m",
		"yellow":      "\x1b[43m",
		"white":       "\x1b[47m",
		"brightblack": "\x1b[100m",
		"#5a4410":     "\x1b[48;2;90;68;16m", // muted amber dirty tint
		"#FFFFFF":     "\x1b[48;2;255;255;255m",
		"#abc":        "", // wrong length
		"#gggggg":     "", // not hex
		"":            "",
		"bogus":       "",
	}
	for in, want := range cases {
		if got := ansiBgCode(in); got != want {
			t.Errorf("ansiBgCode(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSgrPrefixEmitsBackground proves sgrPrefixForStyle now honors the Bg
// field (previously ignored), so a dirty-cell tint can actually render.
// dbsavvy-kvk.
func TestSgrPrefixEmitsBackground(t *testing.T) {
	if got := sgrPrefixForStyle(theme.Style{Bg: "brightblack"}); got != "\x1b[100m" {
		t.Fatalf("Bg-only style: got %q, want %q", got, "\x1b[100m")
	}
}

// TestDirtyCellRendersBackgroundTint is the regression for dbsavvy-kvk: an
// edited cell under the real default theme must carry a background SGR that
// a clean cell does not, so the whole cell reads as dirty even when the
// trailing marker is truncated by an overflowing value.
func TestDirtyCellRendersBackgroundTint(t *testing.T) {
	col := models.ColumnMeta{Name: "c", TypeName: "text"}
	clean := renderCellPadded("hello", col, 10, false)
	dirty := renderCellPadded("hello", col, 10, true)

	bg := sgrPrefixForStyle(theme.Style{Bg: theme.Current().DirtyCellBg.Bg})
	if bg == "" {
		t.Fatalf("default theme DirtyCellBg.Bg=%q produced no SGR — theme misconfigured for 8-colour mode", theme.Current().DirtyCellBg.Bg)
	}
	if !strings.Contains(dirty, bg) {
		t.Errorf("dirty cell %q missing background tint %q", dirty, bg)
	}
	if strings.Contains(clean, bg) {
		t.Errorf("clean cell %q must not carry the dirty tint", clean)
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

// TestRenderCellWithDirty_DirtyTintsDecoratedOnly proves the dirty path
// leaves the visible string unchanged (no glyph) while layering the
// DirtyCellBg background tint onto the decorated string. dbsavvy-kvk.
func TestRenderCellWithDirty_DirtyTintsDecoratedOnly(t *testing.T) {
	col := models.ColumnMeta{Name: "name", TypeName: "text"}
	visClean, _ := renderCell("hello", col)
	vis, dec := renderCellWithDirty("hello", col, true)
	if vis != visClean {
		t.Fatalf("visible %q must match clean %q (no glyph)", vis, visClean)
	}
	bg := ansiBgCode(theme.Current().DirtyCellBg.Bg)
	if bg == "" || !strings.Contains(dec, bg) {
		t.Fatalf("decorated %q must carry the dirty tint %q", dec, bg)
	}
}
