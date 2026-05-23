package grid

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// TestPopulateForeignKeyFlags is the table-driven AC matrix for B3. Each
// case names an AC from dbsavvy-bwq.14 so a regression points straight at
// the offending rule.
func TestPopulateForeignKeyFlags(t *testing.T) {
	publicUsers := models.Ref{Schema: "public", Table: "users"}

	tests := []struct {
		name     string
		cols     []models.ColumnMeta
		fks      []models.ForeignKey
		base     models.Ref
		expected []bool // IsForeignKey expectation per column, positional
	}{
		{
			// AC: PopulateForeignKeyFlags sets IsForeignKey=true only for
			// columns whose name matches FK.Columns and FK.Schema+Table==base.
			name: "single-column FK on base table",
			cols: []models.ColumnMeta{
				{Name: "id"},
				{Name: "team_id"},
				{Name: "name"},
			},
			fks: []models.ForeignKey{
				{
					Schema: "public", Table: "users",
					Columns:    []string{"team_id"},
					RefSchema:  "public",
					RefTable:   "teams",
					RefColumns: []string{"id"},
				},
			},
			base:     publicUsers,
			expected: []bool{false, true, false},
		},
		{
			// AC: Composite FK: every participating column has IsForeignKey=true.
			name: "composite FK flags every participating column",
			cols: []models.ColumnMeta{
				{Name: "tenant_id"},
				{Name: "user_id"},
				{Name: "value"},
			},
			fks: []models.ForeignKey{
				{
					Schema: "public", Table: "users",
					Columns:    []string{"tenant_id", "user_id"},
					RefSchema:  "public",
					RefTable:   "memberships",
					RefColumns: []string{"tenant_id", "user_id"},
				},
			},
			base:     publicUsers,
			expected: []bool{true, true, false},
		},
		{
			// AC: Self-referencing FK: same column gets IsForeignKey=true
			// even though Refs to same table.
			name: "self-FK flags the referencing column",
			cols: []models.ColumnMeta{
				{Name: "id"},
				{Name: "manager_id"},
			},
			fks: []models.ForeignKey{
				{
					Schema: "public", Table: "users",
					Columns:    []string{"manager_id"},
					RefSchema:  "public",
					RefTable:   "users",
					RefColumns: []string{"id"},
				},
			},
			base:     publicUsers,
			expected: []bool{false, true},
		},
		{
			// AC: Empty FK list: all columns IsForeignKey=false.
			name:     "empty FK list",
			cols:     []models.ColumnMeta{{Name: "id"}, {Name: "team_id"}},
			fks:      nil,
			base:     publicUsers,
			expected: []bool{false, false},
		},
		{
			// AC: Non-FK columns retain IsForeignKey=false / Column
			// appearing in result SELECT but not in any FK.
			name: "result column absent from FK list stays unflagged",
			cols: []models.ColumnMeta{
				{Name: "team_id"},
				{Name: "irrelevant"},
			},
			fks: []models.ForeignKey{
				{
					Schema: "public", Table: "users",
					Columns: []string{"team_id"},
				},
			},
			base:     publicUsers,
			expected: []bool{true, false},
		},
		{
			// FK belongs to a different table (e.g. result joins orders.user_id
			// referencing public.users; for grid over `users`, that FK is NOT
			// owned by the base table and must NOT flag any column).
			name: "FK owned by a different table does not flag",
			cols: []models.ColumnMeta{{Name: "team_id"}},
			fks: []models.ForeignKey{
				{
					Schema: "public", Table: "orders",
					Columns: []string{"team_id"},
				},
			},
			base:     publicUsers,
			expected: []bool{false},
		},
		{
			// AC: When result is non-editable (baseTable unresolved), no
			// flags set. We model "unresolved" as Ref{}.
			name: "unresolved base table sets no flags",
			cols: []models.ColumnMeta{{Name: "team_id"}},
			fks: []models.ForeignKey{
				{
					Schema: "public", Table: "users",
					Columns: []string{"team_id"},
				},
			},
			base:     models.Ref{},
			expected: []bool{false},
		},
		{
			// Stale IsForeignKey on the input must be cleared so a fresh
			// run can't leave residue when the column no longer matches.
			name: "stale IsForeignKey input is reset when no FK matches",
			cols: []models.ColumnMeta{
				{Name: "id", IsForeignKey: true},
				{Name: "team_id", IsForeignKey: true},
			},
			fks:      nil,
			base:     publicUsers,
			expected: []bool{false, false},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := PopulateForeignKeyFlags(tc.cols, tc.fks, tc.base)
			require.Len(t, out, len(tc.expected))
			for i, want := range tc.expected {
				require.Equalf(t, want, out[i].IsForeignKey,
					"col[%d] (%q) IsForeignKey: want=%v got=%v",
					i, out[i].Name, want, out[i].IsForeignKey)
			}
		})
	}
}

// TestPopulateForeignKeyFlags_DoesNotMutateInput guards the documented
// contract: callers can pass the same slice they received from the driver
// without worrying about side effects.
func TestPopulateForeignKeyFlags_DoesNotMutateInput(t *testing.T) {
	cols := []models.ColumnMeta{{Name: "team_id"}}
	fks := []models.ForeignKey{
		{Schema: "public", Table: "users", Columns: []string{"team_id"}},
	}
	_ = PopulateForeignKeyFlags(cols, fks, models.Ref{Schema: "public", Table: "users"})
	require.False(t, cols[0].IsForeignKey,
		"input slice must not be mutated; caller installs the returned slice")
}

// TestHeaderRendersFKGlyph confirms the AC: "Grid header renders `→ ` prefix
// for FK columns". Drives a View with two cols (one FK, one not), forces a
// render through the snapshot, and asserts the header line contains the
// glyph adjacent to the FK column's name and not the other.
func TestHeaderRendersFKGlyph(t *testing.T) {
	v := NewView()
	cols := []models.ColumnMeta{
		{Name: "id"},
		{Name: "team_id", IsForeignKey: true},
	}
	v.SetColumns(cols)
	// Seed widths via AppendRows so effectiveWidth returns the locked value
	// (post auto-size) and the header line is laid out with stable widths.
	v.AppendRows([]models.Row{{Values: []any{1, 2}}})

	snap := v.snapshot()
	line := renderHeaderLine(snap, 80)

	require.Contains(t, line, fkHeaderPrefix+"team_id",
		"FK column header must carry the → prefix")
	// The non-FK column must NOT be prefixed with the glyph (the rune may
	// appear elsewhere in tests by accident — check the exact "→ id" form).
	require.NotContains(t, line, fkHeaderPrefix+"id ",
		"non-FK column header must not be decorated")
}

// TestHeaderWidth_AccommodatesGlyph satisfies the "wide column with long
// header + → prefix" AC: when a column's name is long enough that the
// header would otherwise sit at MinColumnWidth, the locked width must grow
// to cover Name + the 2-rune prefix without truncation.
func TestHeaderWidth_AccommodatesGlyph(t *testing.T) {
	const longName = "external_team_reference_id" // 26 runes
	v := NewView()
	cols := []models.ColumnMeta{
		{Name: longName, IsForeignKey: true},
	}
	v.SetColumns(cols)
	// One narrow data cell — keeps the cell-side width small so the header
	// label is the binding constraint on the locked column width.
	v.AppendRows([]models.Row{{Values: []any{"x"}}})

	snap := v.snapshot()
	w := effectiveWidth(snap.widths, 0)
	wantMin := len([]rune(fkHeaderPrefix + longName))
	if wantMin > MaxColumnWidth {
		wantMin = MaxColumnWidth
	}
	require.GreaterOrEqualf(t, w, wantMin,
		"locked width %d must accommodate `→ ` + name (%d runes)", w, wantMin)

	line := renderHeaderLine(snap, w+4)
	require.Contains(t, line, fkHeaderPrefix+longName,
		"full header label must render untruncated when width permits")
}

// TestHeaderRender_NoFKColumns_NoGlyphsLeak guards against accidental
// pollution: when no column is flagged, the rendered header must not
// contain the U+2192 rune anywhere.
func TestHeaderRender_NoFKColumns_NoGlyphsLeak(t *testing.T) {
	v := NewView()
	v.SetColumns([]models.ColumnMeta{
		{Name: "id"},
		{Name: "name"},
	})
	v.AppendRows([]models.Row{{Values: []any{1, "a"}}})

	snap := v.snapshot()
	line := renderHeaderLine(snap, 80)
	require.False(t, strings.Contains(line, fkHeaderPrefix),
		"no FK column ⇒ no `→ ` glyph in header")
}
