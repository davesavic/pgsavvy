package helpers

import (
	"fmt"
	"strings"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// PrepareForwardFK uses a captured projection and row, never a live tab.
func PrepareForwardFK(fks []models.ForeignKey, cols []string, values []any, cursorCol, limit int) (models.Query, string, string, error) {
	if cursorCol < 0 || cursorCol >= len(cols) || len(values) < len(cols) {
		return models.Query{}, "", "", ErrRowNotLoaded
	}
	if cols[cursorCol] == "" {
		return models.Query{}, "", "", models.ErrEditIdentityUnresolved
	}
	matches := matchingFKs(fks, cols[cursorCol])
	if len(matches) == 0 {
		return models.Query{}, "", "", fmt.Errorf("%w: %s", ErrFKNotFound, cols[cursorCol])
	}
	fk := matches[0]
	if len(fk.Columns) == 0 || len(fk.Columns) != len(fk.RefColumns) {
		return models.Query{}, "", "", ErrCompositeMissingColumns
	}
	args := make([]any, len(fk.Columns))
	for i, name := range fk.Columns {
		index := -1
		for j, projected := range cols {
			if projected == name {
				if index >= 0 {
					return models.Query{}, "", "", fmt.Errorf("%w: ambiguous column %s", ErrCompositeMissingColumns, name)
				}
				index = j
			}
		}
		if index < 0 {
			return models.Query{}, "", "", fmt.Errorf("%w: %s", ErrCompositeMissingColumns, name)
		}
		if values[index] == nil {
			return models.Query{}, "", "", fmt.Errorf("%w: %s", ErrFKValueNull, name)
		}
		args[i] = values[index]
	}
	if limit <= 0 {
		limit = DefaultFKForwardLimit
	}
	notice := ""
	if len(matches) > 1 {
		notice = fmt.Sprintf("multiple FKs on column %q; using %s", cols[cursorCol], fk.Name)
	}
	return models.Query{SQL: buildFKForwardSQL(fk, limit), Args: args}, buildFKForwardLabel(fk, args), strings.TrimSpace(notice), nil
}
