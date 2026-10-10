package controllers

import "github.com/davesavic/pgsavvy/pkg/models"

func reverseReferencedValues(fk models.ForeignKey, cols []models.ColumnMeta, row []any, relationOID uint32) ([]any, string) {
	if relationOID == 0 || len(fk.RefColumns) == 0 || len(fk.Columns) != len(fk.RefColumns) {
		return nil, "referenced column metadata is incomplete"
	}
	values := make([]any, len(fk.RefColumns))
	for i, name := range fk.RefColumns {
		match := -1
		for j, col := range cols {
			if col.TableOID != relationOID || col.PhysicalName != name {
				continue
			}
			if match >= 0 {
				return nil, "referenced column is projected more than once: " + name
			}
			match = j
		}
		if match < 0 || match >= len(row) {
			return nil, "referenced column is not loaded: " + name
		}
		values[i] = row[match]
	}
	return values, ""
}
