package ui

import "github.com/davesavic/pgsavvy/pkg/models"

// qualifyHideColumnNames builds the per-column labels shown in the
// <leader>gH hide-cols overlay. When the result spans more than one
// distinct source table (more than one distinct non-zero TableOID),
// every column is qualified with its resolved table name
// ("table.column") so duplicate column names across joined tables are
// distinguishable. For single-table (or table-less) results the bare
// column names are returned unchanged.
//
// oidNames maps a column's TableOID to its resolved relation name. A nil
// or partial map degrades gracefully: any column whose TableOID is 0
// (computed expression) or absent from oidNames falls back to its bare
// name. This lets the overlay render bare names instantly and re-render
// with qualified names once the lazy OID->relname resolution completes.
func qualifyHideColumnNames(cols []models.ColumnMeta, oidNames map[uint32]string) []string {
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.Name
	}
	if !spansMultipleTables(cols) {
		return names
	}
	for i, c := range cols {
		if c.TableOID == 0 {
			continue
		}
		if rel, ok := oidNames[c.TableOID]; ok && rel != "" {
			names[i] = rel + "." + c.Name
		}
	}
	return names
}

// distinctTableOIDs returns the distinct non-zero source-table OIDs of
// cols, in first-seen order. Used to feed the lazy OID->relname resolver.
func distinctTableOIDs(cols []models.ColumnMeta) []uint32 {
	seen := make(map[uint32]struct{}, len(cols))
	var out []uint32
	for _, c := range cols {
		if c.TableOID == 0 {
			continue
		}
		if _, ok := seen[c.TableOID]; ok {
			continue
		}
		seen[c.TableOID] = struct{}{}
		out = append(out, c.TableOID)
	}
	return out
}

// spansMultipleTables reports whether cols reference more than one
// distinct non-zero source table OID.
func spansMultipleTables(cols []models.ColumnMeta) bool {
	seen := make(map[uint32]struct{}, 2)
	for _, c := range cols {
		if c.TableOID == 0 {
			continue
		}
		seen[c.TableOID] = struct{}{}
		if len(seen) > 1 {
			return true
		}
	}
	return false
}
