package exporter

import (
	"encoding/json"
	"io"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// jsonArrayFormat implements Format for a compact JSON array.
//
// Buffered: rows accumulate in memory; Footer emits the array as a single
// compact JSON document (no extra whitespace, terminated by json.Encoder's
// trailing newline). HTML escaping is disabled so '<', '>' and '&' survive
// unescaped in string values.
//
// Like NDJSON, we rely on encoding/json's built-in escaping rather than
// grid.SanitizeCellEscapes.
type jsonArrayFormat struct {
	cols []models.ColumnMeta
	rows []map[string]any
}

// NewJSONArray returns a buffered JSON-array writer.
func NewJSONArray() Format { return &jsonArrayFormat{} }

func (j *jsonArrayFormat) Name() string      { return "JSON Array" }
func (j *jsonArrayFormat) Ext() string       { return "json" }
func (j *jsonArrayFormat) IsStreaming() bool { return false }

func (j *jsonArrayFormat) Header(cols []models.ColumnMeta, _ io.Writer) error {
	j.cols = cols
	j.rows = j.rows[:0]
	return nil
}

func (j *jsonArrayFormat) Row(r models.Row, _ io.Writer) error {
	obj := make(map[string]any, len(j.cols))
	for i, c := range j.cols {
		if i < len(r.Values) {
			obj[c.Name] = jsonSafeValue(r.Values[i], c)
		} else {
			obj[c.Name] = nil
		}
	}
	j.rows = append(j.rows, obj)
	return nil
}

func (j *jsonArrayFormat) Footer(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if j.rows == nil {
		j.rows = []map[string]any{}
	}
	return enc.Encode(j.rows)
}
