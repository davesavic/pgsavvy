package exporter

import (
	"io"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// tsvFormat implements Format for tab-separated values output.
// Quoting follows the same RFC 4180 rules as CSV but with '\t' as the
// trigger delimiter.
type tsvFormat struct {
	cols []models.ColumnMeta
}

// NewTSV returns a streaming TSV writer.
func NewTSV() Format { return &tsvFormat{} }

func (t *tsvFormat) Name() string      { return "TSV" }
func (t *tsvFormat) Ext() string       { return "tsv" }
func (t *tsvFormat) IsStreaming() bool { return true }

func (t *tsvFormat) Header(cols []models.ColumnMeta, w io.Writer) error {
	t.cols = cols
	return writeDelimitedRow(w, columnNames(cols), '\t')
}

func (t *tsvFormat) Row(r models.Row, w io.Writer) error {
	fields := make([]string, len(r.Values))
	for i, v := range r.Values {
		if i < len(t.cols) {
			fields[i] = stringifyAndSanitize(v, t.cols[i])
		} else {
			fields[i] = stringifyAndSanitize(v, models.ColumnMeta{})
		}
	}
	return writeDelimitedRow(w, fields, '\t')
}

func (t *tsvFormat) Footer(w io.Writer) error { return nil }
