package exporter

import (
	"fmt"
	"io"
	"strings"

	"github.com/davesavic/dbsavvy/pkg/gui/grid"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// csvFormat implements Format for RFC 4180 CSV output.
type csvFormat struct{}

// NewCSV returns a streaming RFC 4180 CSV writer.
func NewCSV() Format { return csvFormat{} }

func (csvFormat) Name() string      { return "CSV" }
func (csvFormat) Ext() string       { return "csv" }
func (csvFormat) IsStreaming() bool { return true }

func (csvFormat) Header(cols []models.ColumnMeta, w io.Writer) error {
	return writeDelimitedRow(w, columnNames(cols), ',')
}

func (csvFormat) Row(r models.Row, w io.Writer) error {
	fields := make([]string, len(r.Values))
	for i, v := range r.Values {
		fields[i] = stringifyAndSanitize(v)
	}
	return writeDelimitedRow(w, fields, ',')
}

func (csvFormat) Footer(w io.Writer) error { return nil }

// columnNames extracts and sanitizes column names from metadata.
func columnNames(cols []models.ColumnMeta) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = grid.SanitizeCellEscapes(c.Name)
	}
	return out
}

// stringifyAndSanitize converts a cell value to its sanitized string form.
// nil → empty string (NULL convention matches psql \copy csv default).
func stringifyAndSanitize(v any) string {
	if v == nil {
		return ""
	}
	s := fmt.Sprint(v)
	return grid.SanitizeCellEscapes(s)
}

// writeDelimitedRow writes a single delimited row terminated by CRLF.
// Shared by CSV (delim=',') and TSV (delim='\t').
func writeDelimitedRow(w io.Writer, fields []string, delim byte) error {
	var b strings.Builder
	for i, f := range fields {
		if i > 0 {
			b.WriteByte(delim)
		}
		b.WriteString(rfc4180Quote(f, delim))
	}
	b.WriteString("\r\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// rfc4180Quote wraps a field in double quotes when it contains the delimiter,
// a double quote, CR or LF. Embedded double quotes are doubled.
func rfc4180Quote(s string, delim byte) string {
	if !strings.ContainsAny(s, "\r\n\"") && !strings.ContainsRune(s, rune(delim)) {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
