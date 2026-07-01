package exporter

import (
	"encoding/json"

	"github.com/davesavic/pgsavvy/pkg/gui/grid"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// jsonSafeValue converts a cell value for safe embedding in JSON-format
// exports. For json/jsonb columns, []byte values are converted to
// json.RawMessage so the encoder embeds them as JSON objects rather than
// base64-encoding the byte slice. All other values pass through unchanged.
func jsonSafeValue(v any, col models.ColumnMeta) any {
	if !grid.IsJSONColumn(col) {
		return v
	}
	b, ok := v.([]byte)
	if !ok {
		return v
	}
	return json.RawMessage(b)
}
