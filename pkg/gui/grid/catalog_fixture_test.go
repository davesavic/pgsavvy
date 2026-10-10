package grid

import "github.com/davesavic/pgsavvy/pkg/models"

func catalogFixtureColumns(cols []models.ColumnMeta) []models.ColumnMeta {
	resolved := append([]models.ColumnMeta(nil), cols...)
	for i := range resolved {
		resolved[i].PhysicalName = resolved[i].Name
		resolved[i].TableOID = 42
		resolved[i].TableAttributeNumber = uint16(i + 1)
	}
	return resolved
}
