package exporter

import (
	"encoding/json"
	"io"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// ndjsonFormat implements Format for newline-delimited JSON output.
//
// Streaming: each call to Row emits one JSON object followed by a newline
// (json.Encoder.Encode appends '\n' itself). Header is a no-op aside from
// capturing column names; Footer is a no-op.
//
// Escaping note: we intentionally rely on encoding/json's built-in escaping
// for control characters and invalid UTF-8 (json.Marshal replaces invalid
// bytes with U+FFFD). We do NOT route values through grid.SanitizeCellEscapes
// because JSON string encoding already handles \x1b, newlines, etc.
//
// We disable HTML escaping (SetEscapeHTML(false)) so that '<', '>' and '&'
// survive unescaped in string values.
type ndjsonFormat struct {
	cols []models.ColumnMeta
}

// NewNDJSON returns a streaming newline-delimited JSON writer.
func NewNDJSON() Format { return &ndjsonFormat{} }

func (n *ndjsonFormat) Name() string      { return "NDJSON" }
func (n *ndjsonFormat) Ext() string       { return "ndjson" }
func (n *ndjsonFormat) IsStreaming() bool { return true }

func (n *ndjsonFormat) Header(cols []models.ColumnMeta, _ io.Writer) error {
	n.cols = cols
	return nil
}

func (n *ndjsonFormat) Row(r models.Row, w io.Writer) error {
	obj := make(map[string]any, len(n.cols))
	for i, c := range n.cols {
		if i < len(r.Values) {
			obj[c.Name] = r.Values[i]
		} else {
			obj[c.Name] = nil
		}
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(obj)
}

func (n *ndjsonFormat) Footer(_ io.Writer) error { return nil }
