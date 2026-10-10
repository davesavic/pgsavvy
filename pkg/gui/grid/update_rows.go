package grid

import (
	"github.com/davesavic/pgsavvy/pkg/models"
)

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
	return v.updateRowsByPK(refCols, refRows, nil)
}

// UpdateRowsFromOriginalPK maps each refreshed row to its pre-commit identity.
// This keeps key changes from replacing a different buffered row.
func (v *View) UpdateRowsFromOriginalPK(identity models.EditIdentity, refCols []models.ColumnMeta, refRows []models.Row, originalPKs [][]any) int {
	if len(originalPKs) != len(refRows) {
		return 0
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	keys, ok := v.normalizeOriginalKeysLocked(identity, originalPKs)
	if !ok {
		return 0
	}
	v.editBaseline++
	return v.updateRowsByPKLocked(refCols, refRows, keys)
}

func (v *View) normalizeOriginalKeysLocked(identity models.EditIdentity, originalPKs [][]any) ([][]any, bool) {
	target := v.editIdentityLocked()
	keys := make([][]any, len(originalPKs))
	for i, pk := range originalPKs {
		var err error
		keys[i], err = target.Normalize(identity, pk)
		if err != nil {
			return nil, false
		}
	}
	return keys, target.Compatible(identity)
}

func (v *View) updateRowsByPK(refCols []models.ColumnMeta, refRows []models.Row, originalPKs [][]any) int {
	if len(refCols) == 0 || len(refRows) == 0 {
		return 0
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	return v.updateRowsByPKLocked(refCols, refRows, originalPKs)
}

func (v *View) updateRowsByPKLocked(refCols []models.ColumnMeta, refRows []models.Row, originalPKs [][]any) int {
	if len(refCols) == 0 || len(refRows) == 0 {
		return 0
	}
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
		ri, ok := refIdx[v.cols[idx].PhysicalName]
		if !ok && originalPKs == nil {
			return 0
		}
		pkRefIdx[i] = ri
	}

	updated := make(map[int]models.Row)
	for r, ref := range refRows {
		pk := make([]any, len(pkRefIdx))
		skip := false
		for i, ri := range pkRefIdx {
			if ri >= len(ref.Values) {
				skip = true
				break
			}
			pk[i] = ref.Values[ri]
		}
		if originalPKs != nil {
			pk = originalPKs[r]
			skip = len(pk) != len(v.rowIdentity)
		}
		if skip {
			continue
		}
		for gi, row := range v.rows {
			matches := pkSliceEqual(rowPKValues(row, v.rowIdentity), pk)
			if originalPKs != nil {
				matches = models.RowIdentityEqual(rowPKValues(row, v.rowIdentity), pk)
			}
			if !matches {
				continue
			}
			vals := make([]any, len(row.Values))
			copy(vals, row.Values)
			for ci, col := range v.cols {
				ri, ok := refIdx[col.PhysicalName]
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
	v.editBaseline++
	return len(updated)
}
