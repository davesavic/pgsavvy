package exporter

import (
	"io"
	"strings"

	"github.com/davesavic/pgsavvy/pkg/gui/grid"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// csvFormat implements Format for RFC 4180 CSV output.
type csvFormat struct {
	cols []models.ColumnMeta
}

// NewCSV returns a streaming RFC 4180 CSV writer.
func NewCSV() Format { return &csvFormat{} }

func (c *csvFormat) Name() string      { return "CSV" }
func (c *csvFormat) Ext() string       { return "csv" }
func (c *csvFormat) IsStreaming() bool { return true }

func (c *csvFormat) Header(cols []models.ColumnMeta, w io.Writer) error {
	c.cols = cols
	return writeDelimitedRow(w, columnNames(cols), ',')
}

func (c *csvFormat) Row(r models.Row, w io.Writer) error {
	var b strings.Builder
	for i, v := range r.Values {
		if i > 0 {
			b.WriteByte(',')
		}
		if v == nil {
			continue
		}
		var col models.ColumnMeta
		if i < len(c.cols) {
			col = c.cols[i]
		}
		field := stringifyAndSanitize(v, col)
		if field == "" {
			// COPY CSV distinguishes quoted empty text from unquoted NULL.
			b.WriteString(`""`)
		} else {
			b.WriteString(grid.Rfc4180Quote(field, ','))
		}
	}
	b.WriteString("\r\n")
	_, err := io.WriteString(w, b.String())
	return err
}

func (c *csvFormat) Footer(w io.Writer) error { return nil }

// columnNames extracts and sanitizes column names from metadata.
func columnNames(cols []models.ColumnMeta) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = grid.SanitizeCellEscapes(c.Name)
	}
	return out
}

// stringifyAndSanitize converts a cell value to its sanitized string form
// using column-aware formatting. nil → empty string (NULL convention
// matches psql \copy csv default).
func stringifyAndSanitize(v any, col models.ColumnMeta) string {
	if v == nil {
		return ""
	}
	return grid.RenderRetainedCellText(v, col)
}

// writeDelimitedRow writes a single delimited row terminated by CRLF.
// Shared by CSV (delim=',') and TSV (delim='\t').
func writeDelimitedRow(w io.Writer, fields []string, delim byte) error {
	var b strings.Builder
	for i, f := range fields {
		if i > 0 {
			b.WriteByte(delim)
		}
		b.WriteString(grid.Rfc4180Quote(f, delim))
	}
	b.WriteString("\r\n")
	_, err := io.WriteString(w, b.String())
	return err
}
