package ui

import "github.com/davesavic/pgsavvy/pkg/models"

func resolvedFixtureColumns(cols []models.ColumnMeta) []models.ColumnMeta {
	resolved := append([]models.ColumnMeta(nil), cols...)
	for i := range resolved {
		resolved[i].PhysicalName = resolved[i].Name
	}
	return resolved
}
