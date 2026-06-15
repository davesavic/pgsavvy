package grid

import "github.com/davesavic/pgsavvy/pkg/models"

// UpdateRowsByPK overwrites buffered cell values with refetched
// post-commit data, matching rows on the grid's row identity and columns
// by NAME — the refetch projection is SELECT * against the base table
// while the grid may render an arbitrary subset/order. Refetched rows
// whose identity matches no buffered row, and grid columns absent from
// the refetch projection, are left untouched. Returns the number of
// buffered rows updated.
//
// The row buffer is replaced wholesale (fresh backing array, fresh
// Values slices for updated rows) rather than mutated in place, so an
// in-flight Render holding a snapshot never observes a torn row — the
// same discipline snapshot() documents for cols+widths.
func (v *View) UpdateRowsByPK(refCols []models.ColumnMeta, refRows []models.Row) int {
	if len(refCols) == 0 || len(refRows) == 0 {
		return 0
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.rowIdentity) == 0 || len(v.rows) == 0 {
		return 0
	}

	refIdx := make(map[string]int, len(refCols))
	for i, c := range refCols {
		refIdx[c.Name] = i
	}

	// Identity column positions inside the refetch projection. A missing
	// identity column means no refetched row can be addressed safely.
	pkRefIdx := make([]int, len(v.rowIdentity))
	for i, idx := range v.rowIdentity {
		if idx < 0 || idx >= len(v.cols) {
			return 0
		}
		ri, ok := refIdx[v.cols[idx].Name]
		if !ok {
			return 0
		}
		pkRefIdx[i] = ri
	}

	updated := make(map[int]models.Row)
	for _, ref := range refRows {
		pk := make([]any, len(pkRefIdx))
		skip := false
		for i, ri := range pkRefIdx {
			if ri >= len(ref.Values) {
				skip = true
				break
			}
			pk[i] = ref.Values[ri]
		}
		if skip {
			continue
		}
		for gi, row := range v.rows {
			if !pkSliceEqual(rowPKValues(row, v.rowIdentity), pk) {
				continue
			}
			vals := make([]any, len(row.Values))
			copy(vals, row.Values)
			for ci, col := range v.cols {
				ri, ok := refIdx[col.Name]
				if !ok || ci >= len(vals) || ri >= len(ref.Values) {
					continue
				}
				vals[ci] = ref.Values[ri]
			}
			updated[gi] = models.Row{Values: vals}
		}
	}
	if len(updated) == 0 {
		return 0
	}

	rows := make([]models.Row, len(v.rows))
	copy(rows, v.rows)
	for gi, row := range updated {
		rows[gi] = row
	}
	v.rows = rows
	return len(updated)
}
