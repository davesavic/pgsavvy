package exporter

import (
	"io"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// tsvFormat implements Format for tab-separated values output.
// Quoting follows the same RFC 4180 rules as CSV but with '\t' as the
// trigger delimiter.
type tsvFormat struct{}

// NewTSV returns a streaming TSV writer.
func NewTSV() Format { return tsvFormat{} }

func (tsvFormat) Name() string      { return "TSV" }
func (tsvFormat) Ext() string       { return "tsv" }
func (tsvFormat) IsStreaming() bool { return true }

func (tsvFormat) Header(cols []models.ColumnMeta, w io.Writer) error {
	return writeDelimitedRow(w, columnNames(cols), '\t')
}

func (tsvFormat) Row(r models.Row, w io.Writer) error {
	fields := make([]string, len(r.Values))
	for i, v := range r.Values {
		fields[i] = stringifyAndSanitize(v)
	}
	return writeDelimitedRow(w, fields, '\t')
}

func (tsvFormat) Footer(w io.Writer) error { return nil }
