package grid

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/models"
)

func TestUpdateRowsByPK(t *testing.T) {
	cols := func(names ...string) []models.ColumnMeta {
		out := make([]models.ColumnMeta, len(names))
		for i, n := range names {
			out[i] = models.ColumnMeta{Name: n}
		}
		return out
	}

	tests := []struct {
		name        string
		gridCols    []models.ColumnMeta
		rowIdentity []int
		gridRows    []models.Row
		refCols     []models.ColumnMeta
		refRows     []models.Row
		wantUpdated int
		wantRows    []models.Row
	}{
		{
			name:        "updates matching row, columns matched by name across differing projections",
			gridCols:    cols("id", "payload"),
			rowIdentity: []int{0},
			gridRows: []models.Row{
				{Values: []any{int64(1), `{"plan":"pro"}`}},
				{Values: []any{int64(2), `{"plan":"basic"}`}},
			},
			// Refetch is SELECT * — different column order plus a column
			// the grid projection does not show.
			refCols: cols("payload", "id", "extra"),
			refRows: []models.Row{
				{Values: []any{`{"plan":"pro2"}`, int64(1), "x"}},
			},
			wantUpdated: 1,
			wantRows: []models.Row{
				{Values: []any{int64(1), `{"plan":"pro2"}`}},
				{Values: []any{int64(2), `{"plan":"basic"}`}},
			},
		},
		{
			name:        "composite pk matches on every identity column",
			gridCols:    cols("a", "b", "val"),
			rowIdentity: []int{0, 1},
			gridRows: []models.Row{
				{Values: []any{int64(1), int64(1), "old-11"}},
				{Values: []any{int64(1), int64(2), "old-12"}},
			},
			refCols: cols("a", "b", "val"),
			refRows: []models.Row{
				{Values: []any{int64(1), int64(2), "new-12"}},
			},
			wantUpdated: 1,
			wantRows: []models.Row{
				{Values: []any{int64(1), int64(1), "old-11"}},
				{Values: []any{int64(1), int64(2), "new-12"}},
			},
		},
		{
			name:        "no row identity is a no-op",
			gridCols:    cols("id", "payload"),
			rowIdentity: nil,
			gridRows:    []models.Row{{Values: []any{int64(1), "old"}}},
			refCols:     cols("id", "payload"),
			refRows:     []models.Row{{Values: []any{int64(1), "new"}}},
			wantUpdated: 0,
			wantRows:    []models.Row{{Values: []any{int64(1), "old"}}},
		},
		{
			name:        "refetch missing a pk column skips that refetched row",
			gridCols:    cols("id", "payload"),
			rowIdentity: []int{0},
			gridRows:    []models.Row{{Values: []any{int64(1), "old"}}},
			refCols:     cols("payload"),
			refRows:     []models.Row{{Values: []any{"new"}}},
			wantUpdated: 0,
			wantRows:    []models.Row{{Values: []any{int64(1), "old"}}},
		},
		{
			name:        "non-matching pk leaves buffer untouched",
			gridCols:    cols("id", "payload"),
			rowIdentity: []int{0},
			gridRows:    []models.Row{{Values: []any{int64(1), "old"}}},
			refCols:     cols("id", "payload"),
			refRows:     []models.Row{{Values: []any{int64(99), "new"}}},
			wantUpdated: 0,
			wantRows:    []models.Row{{Values: []any{int64(1), "old"}}},
		},
		{
			name:        "empty refetch is a no-op",
			gridCols:    cols("id", "payload"),
			rowIdentity: []int{0},
			gridRows:    []models.Row{{Values: []any{int64(1), "old"}}},
			refCols:     nil,
			refRows:     nil,
			wantUpdated: 0,
			wantRows:    []models.Row{{Values: []any{int64(1), "old"}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := NewView()
			v.SetColumns(tt.gridCols)
			v.SetEditability(true, tt.rowIdentity, "", "public")
			v.AppendRows(tt.gridRows)

			got := v.UpdateRowsByPK(tt.refCols, tt.refRows)
			if got != tt.wantUpdated {
				t.Fatalf("UpdateRowsByPK returned %d, want %d", got, tt.wantUpdated)
			}

			rows := v.AllRows()
			if len(rows) != len(tt.wantRows) {
				t.Fatalf("row count %d, want %d", len(rows), len(tt.wantRows))
			}
			for r := range rows {
				for c := range rows[r].Values {
					if rows[r].Values[c] != tt.wantRows[r].Values[c] {
						t.Errorf("row %d col %d = %v, want %v",
							r, c, rows[r].Values[c], tt.wantRows[r].Values[c])
					}
				}
			}
		})
	}
}
